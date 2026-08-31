# -*- coding: utf-8 -*-
"""
用户配额/有效期/用户端口池 e2e：
A. 配额：user1 maxPorts=2 + 用户池 20000-20001 → p1/p2 分到端口，p3 创建被拒（端口数量已达上限）；
   删除 p2 释放后 p3 可创建（复用 20001）
B. 用户池收紧：user1 池改 20000-20000 → 配置收紧自动踢线（kicked=true），重连后 p1 保留 20000、
   p3 因池内无可用端口转为未分配（0）
C/D. 有效期：user2 创建（30 天后过期）→ PS 重启换绑 user2 → 在线后建 p9 拿端口；
   改 user2 有效期为昨天 → 30s 内被踢（注册被拒）；改回 30 天后 → 自动重连且 p9 端口不变
"""
import os
import re
import shutil
import subprocess
import sys
import time

import requests

BASE = 'http://127.0.0.1:9998'
PSB = 'http://127.0.0.1:65443'
RT = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'runtime')
PM_DIR = os.path.join(RT, 'pm')
PS_DIR = os.path.join(RT, 'ps')
PM_CONF = os.path.join(PM_DIR, 'conf', 'seeinpm.toml')
PS_CONF = os.path.join(PS_DIR, 'conf', 'seeinps.toml')

procs = []
QUOTA_MSG = '端口数量已达上限，请联系管理员'


def cleanup():
    ts = time.strftime('%H%M%S')
    for d in [os.path.join(PM_DIR, 'data'), os.path.join(PS_DIR, 'data')]:
        for f in os.listdir(d):
            p = os.path.join(d, f)
            if f.endswith('.db') or f.endswith('.db-wal') or f.endswith('.db-shm'):
                try:
                    os.rename(p, p + f'.bak{ts}')
                except OSError:
                    pass
    for d in [os.path.join(PM_DIR, 'logs'), os.path.join(PS_DIR, 'logs')]:
        shutil.rmtree(d, ignore_errors=True)
        os.makedirs(d, exist_ok=True)


def start_pm():
    logpath = os.path.join(PM_DIR, 'logs', 'seeinpm.log')
    f = open(logpath, 'wb', buffering=0)
    p = subprocess.Popen([os.path.join(RT, 'seeinpm.exe'), '-conf', 'conf/seeinpm.toml'], cwd=PM_DIR, stdout=f, stderr=subprocess.STDOUT)
    procs.append(p)
    return p


def start_ps():
    logpath = os.path.join(PS_DIR, 'logs', 'seeinps.log')
    f = open(logpath, 'wb', buffering=0)
    p = subprocess.Popen([os.path.join(RT, 'seeinps.exe'), '-conf', 'conf/seeinps.toml'], cwd=PS_DIR, stdout=f, stderr=subprocess.STDOUT)
    procs.append(p)
    return p


def kill_ps():
    subprocess.run(['taskkill', '/F', '/IM', 'seeinps.exe'], capture_output=True)
    time.sleep(1)
    for p in [x for x in procs if 'seeinps' in str(x.args[0])]:
        procs.remove(p)


def wait_http(url, timeout=20):
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            requests.get(url, timeout=2)
            return True
        except Exception:
            time.sleep(0.5)
    return False


def wait_ps_status(ph, want, timeout=60):
    t0 = time.time()
    last = None
    while time.time() - t0 < timeout:
        try:
            r = requests.get(f'{PSB}/api/v1/status', headers=ph, timeout=3)
            last = r.json().get('data', {}).get('status')
            if last == want:
                return True
        except Exception:
            pass
        time.sleep(2)
    print(f'  timeout waiting status={want}, last={last}')
    return False


def ps_proxies(ph):
    r = requests.get(f'{PSB}/api/v1/proxies', headers=ph, timeout=5)
    return {p.get('id'): p.get('forwardPort') for p in r.json().get('data', [])}


def date_after(days):
    return time.strftime('%Y-%m-%d', time.localtime(time.time() + days * 86400))


def date_before(days):
    return time.strftime('%Y-%m-%d', time.localtime(time.time() - days * 86400))


def main():
    with open(PM_CONF, 'r', encoding='utf-8') as f:
        orig_pm_conf = f.read()
    with open(PS_CONF, 'r', encoding='utf-8') as f:
        orig_ps_conf = f.read()
    try:
        print('=== 0. 启动 PM + PS（user1 无限制） ===')
        cleanup()
        for exe in ['seeinpm.exe', 'seeinps.exe']:
            subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
        time.sleep(1)
        start_pm()
        wait_http(BASE + '/health')
        requests.post(f'{BASE}/api/v1/auth/init', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
        r = requests.post(f'{BASE}/api/v1/auth/login', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
        h = {'Authorization': 'Bearer ' + r.json().get('data', {}).get('access_token', '')}
        r = requests.post(f'{BASE}/api/v1/users', json={'username': 'user1', 'remark': 'e2e'}, headers=h, timeout=5)
        code1 = r.json().get('data', {}).get('authCode', '')
        conf = re.sub(r'auth_code = ".*"', f'auth_code = "{code1}"', orig_ps_conf)
        with open(PS_CONF, 'w', encoding='utf-8') as f:
            f.write(conf)
        start_ps()
        wait_http(PSB + '/health', timeout=25)
        time.sleep(3)
        requests.post(f'{PSB}/api/v1/auth/init', json={'username': 'user1', 'authCode': code1, 'password': 'PsAdmin123'}, timeout=5)
        time.sleep(1)
        r = requests.post(f'{PSB}/api/v1/auth/login', json={'username': 'user1', 'password': 'PsAdmin123'}, timeout=5)
        ph1 = {'Authorization': 'Bearer ' + r.json().get('data', {}).get('token', '')}

        print('=== A1. 配置 user1：maxPorts=2，用户池 20000-20001 ===')
        r = requests.put(f'{BASE}/api/v1/users/user1', json={'expireDate': '', 'maxPorts': 2, 'portRanges': [{'start': 20000, 'end': 20001}]}, headers=h, timeout=5)
        print(' ', r.text[:200])
        assert r.json().get('code') == 0

        print('=== A2. 创建 p1/p2 应各得端口；p3 应被配额拒绝 ===')
        r = requests.post(f'{PSB}/api/v1/proxies', json={'id': 'p1', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 9001}, headers=ph1, timeout=10)
        assert r.json().get('data', {}).get('forwardPort') == 20000, r.text
        r = requests.post(f'{PSB}/api/v1/proxies', json={'id': 'p2', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 9002}, headers=ph1, timeout=10)
        assert r.json().get('data', {}).get('forwardPort') == 20001, r.text
        r = requests.post(f'{PSB}/api/v1/proxies', json={'id': 'p3', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 9003}, headers=ph1, timeout=10)
        print('  p3 ->', r.status_code, r.text[:120])
        assert r.status_code == 502 and QUOTA_MSG in r.text, 'expected quota message'

        print('=== A3. 删除 p2 释放端口后，p3 可创建且复用 20001 ===')
        requests.delete(f'{PSB}/api/v1/proxies/p2', headers=ph1, timeout=5)
        time.sleep(1)
        r = requests.post(f'{PSB}/api/v1/proxies', json={'id': 'p3', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 9003}, headers=ph1, timeout=10)
        assert r.json().get('data', {}).get('forwardPort') == 20001, r.text
        print('  OK: p3 got 20001')

        print('=== B1. 用户池收紧为 20000-20000 → 自动踢线 ===')
        r = requests.put(f'{BASE}/api/v1/users/user1', json={'expireDate': '', 'maxPorts': 2, 'portRanges': [{'start': 20000, 'end': 20000}]}, headers=h, timeout=5)
        print(' ', r.text[:150])
        assert r.json().get('data', {}).get('kicked') is True, 'expected kicked=true'
        time.sleep(2)
        # 等待重连 + 重新分配
        ok = False
        for _ in range(30):
            ports = ps_proxies(ph1)
            if ports.get('p1') == 20000 and ports.get('p3') == 0:
                ok = True
                break
            time.sleep(2)
        print('  ports:', ps_proxies(ph1))
        assert ok, f'expected p1=20000, p3=0, got {ports}'
        print('  OK: p1 kept 20000, p3 unassigned (pool full)')

        print('=== C1. 创建 user2（30 天后过期），PS 换绑 user2 ===')
        r = requests.post(f'{BASE}/api/v1/users', json={'username': 'user2', 'expireDate': date_after(30)}, headers=h, timeout=5)
        code2 = r.json().get('data', {}).get('authCode', '')
        assert code2, r.text
        kill_ps()
        shutil.rmtree(os.path.join(PS_DIR, 'data'), ignore_errors=True)
        os.makedirs(os.path.join(PS_DIR, 'data'), exist_ok=True)
        conf = re.sub(r'username = ".*"', 'username = "user2"', orig_ps_conf)
        conf = re.sub(r'auth_code = ".*"', f'auth_code = "{code2}"', conf)
        with open(PS_CONF, 'w', encoding='utf-8') as f:
            f.write(conf)
        start_ps()
        wait_http(PSB + '/health', timeout=25)
        time.sleep(3)
        requests.post(f'{PSB}/api/v1/auth/init', json={'username': 'user2', 'authCode': code2, 'password': 'PsAdmin123'}, timeout=5)
        time.sleep(1)
        r = requests.post(f'{PSB}/api/v1/auth/login', json={'username': 'user2', 'password': 'PsAdmin123'}, timeout=5)
        ph2 = {'Authorization': 'Bearer ' + r.json().get('data', {}).get('token', '')}
        assert wait_ps_status(ph2, 'connected', 40), 'user2 should connect'
        r = requests.post(f'{PSB}/api/v1/proxies', json={'id': 'p9', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 9009}, headers=ph2, timeout=10)
        port9 = r.json().get('data', {}).get('forwardPort')
        print('  user2 connected, p9 port =', port9)
        assert port9 and port9 > 0, r.text

        print('=== D1. user2 有效期改为昨天 → 30s 内被踢，注册被拒 ===')
        r = requests.put(f'{BASE}/api/v1/users/user2', json={'expireDate': date_before(1), 'maxPorts': 0, 'portRanges': []}, headers=h, timeout=5)
        assert r.json().get('code') == 0
        assert wait_ps_status(ph2, 'disconnected', 60), 'user2 should be kicked after expiry'
        print('  OK: kicked after expiry')
        time.sleep(6)  # 给重连尝试留时间，确认注册被拒（状态保持断开）
        st = requests.get(f'{PSB}/api/v1/status', headers=ph2, timeout=3).json().get('data', {}).get('status')
        assert st == 'disconnected', f'should stay disconnected while expired, got {st}'

        print('=== D2. 有效期改回 30 天后 → 自动重连，p9 端口不变 ===')
        r = requests.put(f'{BASE}/api/v1/users/user2', json={'expireDate': date_after(30), 'maxPorts': 0, 'portRanges': []}, headers=h, timeout=5)
        assert r.json().get('code') == 0
        assert wait_ps_status(ph2, 'connected', 60), 'user2 should reconnect after expiry extended'
        ok = False
        for _ in range(15):
            ports = ps_proxies(ph2)
            if ports.get('p9') == port9:
                ok = True
                break
            time.sleep(2)
        print('  ports:', ps_proxies(ph2))
        assert ok, f'expected p9 port restored to {port9}, got {ports}'
        print('  OK: p9 port stable after recovery')

        print('=== DONE ===')
    finally:
        with open(PM_CONF, 'w', encoding='utf-8') as f:
            f.write(orig_pm_conf)
        with open(PS_CONF, 'w', encoding='utf-8') as f:
            f.write(orig_ps_conf)
        for p in procs:
            p.terminate()
        for exe in ['seeinpm.exe', 'seeinps.exe']:
            subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
        sys.stdout.flush()


if __name__ == '__main__':
    main()
