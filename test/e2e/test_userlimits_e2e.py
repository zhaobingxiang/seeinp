# -*- coding: utf-8 -*-
"""
用户限制（有效期/端口数量配额/用户端口池）e2e：
1. user1 无限制上线；user2 建号 maxPorts=2，双实例上线
2. 非法配置 400：用户池越界总池 / 用户池跨度超过配额
3. user2 建 2 个代理成功，第 3 个被拒（端口数量已达上限）
4. 编辑 user2：配额不限 + 用户池 {20005,20006} → 配置收紧自动踢线，重连后端口落在用户池内，
   第 3 个代理被拒（端口池获取失败——用户池仅 2 端口且已占满）
5. user1 有效期改为昨天 → 30s 定时器断开；改回明天 → seeinps 自动重连恢复全部代理
6. 用户列表字段校验（expireDate/expired/maxPorts/portRanges/usedPorts）
"""
import datetime
import os
import re
import shutil
import subprocess
import sys
import time

import requests

BASE = 'http://127.0.0.1:9998'
PS1 = 'http://127.0.0.1:65443'
PS2 = 'http://127.0.0.1:16444'
RT = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'runtime')
PM_DIR = os.path.join(RT, 'pm')
PS_DIR = os.path.join(RT, 'ps')
PS2_DIR = os.path.join(RT, 'ps2')

procs = []


def cleanup():
    ts = time.strftime('%H%M%S')
    for d in [PM_DIR, PS_DIR, PS2_DIR]:
        datadir = os.path.join(d, 'data')
        if os.path.isdir(datadir):
            for f in os.listdir(datadir):
                p = os.path.join(datadir, f)
                if f.endswith('.db') or f.endswith('.db-wal') or f.endswith('.db-shm'):
                    try:
                        os.rename(p, p + f'.bak{ts}')
                    except OSError:
                        pass
        logdir = os.path.join(d, 'logs')
        shutil.rmtree(logdir, ignore_errors=True)
        os.makedirs(logdir, exist_ok=True)


def start(workdir, exe, conf=None):
    logname = 'seeinpm.log' if 'pm' in workdir else 'seeinps.log'
    if conf is None:
        conf = 'seeinpm.toml' if 'pm' in workdir else 'seeinps.toml'
    f = open(os.path.join(workdir, 'logs', logname), 'wb', buffering=0)
    p = subprocess.Popen([exe, '-conf', 'conf/' + conf], cwd=workdir, stdout=f, stderr=subprocess.STDOUT)
    procs.append(p)
    return p


def wait_http(url, timeout=25):
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            requests.get(url, timeout=2)
            return True
        except Exception:
            time.sleep(0.5)
    return False


def wait_for(fn, timeout, interval=2, desc=''):
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            if fn():
                return True
        except Exception:
            pass
        time.sleep(interval)
    return False


def ps_login(base, local_user='user1'):
    r = requests.post(f'{base}/api/v1/auth/login', json={'username': local_user, 'password': 'PsAdmin123'}, timeout=5)
    return {'Authorization': 'Bearer ' + r.json().get('data', {}).get('token', '')}


def main():
    print('=== 0. 启动 PM + 双 PS 实例 ===')
    cleanup()
    for exe in ['seeinpm.exe', 'seeinps.exe']:
        subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
    time.sleep(1)

    # 第二个 PS 实例目录（user2 用，B 端口 16444）
    if os.path.isdir(PS2_DIR):
        shutil.rmtree(PS2_DIR)
    shutil.copytree(PS_DIR, PS2_DIR)

    start(PM_DIR, os.path.join(RT, 'seeinpm.exe'))
    wait_http(BASE + '/health')
    requests.post(f'{BASE}/api/v1/auth/init', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
    r = requests.post(f'{BASE}/api/v1/auth/login', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
    h = {'Authorization': f'Bearer {r.json().get("data", {}).get("access_token", "")}'}

    r = requests.post(f'{BASE}/api/v1/users', json={'username': 'user1', 'remark': 'e2e'}, headers=h, timeout=5)
    code1 = r.json().get('data', {}).get('authCode', '')
    print('=== 1. 非法用户配置 400 ===')
    r = requests.post(f'{BASE}/api/v1/users', json={'username': 'user2', 'portRanges': [{'start': 40000, 'end': 40010}]}, headers=h, timeout=5)
    print('  outside global pool ->', r.status_code, r.text[:120])
    assert r.status_code == 400 and '不在总端口池' in r.text, 'expect user pool outside global rejected'
    r = requests.post(f'{BASE}/api/v1/users', json={'username': 'user2', 'maxPorts': 2, 'portRanges': [{'start': 20005, 'end': 20008}]}, headers=h, timeout=5)
    print('  span 4 > quota 2 ->', r.status_code, r.text[:120])
    assert r.status_code == 400 and '超过端口数量配额' in r.text, 'expect span>quota rejected'

    r = requests.post(f'{BASE}/api/v1/users', json={'username': 'user2', 'maxPorts': 2, 'remark': 'quota2'}, headers=h, timeout=5)
    assert r.status_code == 200, r.text
    code2 = r.json().get('data', {}).get('authCode', '')

    for d, code in [(PS_DIR, code1), (PS2_DIR, code2)]:
        conf_path = os.path.join(d, 'conf', 'seeinps.toml')
        with open(conf_path, 'r', encoding='utf-8') as f:
            conf = f.read()
        conf = re.sub(r'auth_code = ".*"', f'auth_code = "{code}"', conf)
        if d == PS2_DIR:
            conf = re.sub(r'username = ".*"', 'username = "user2"', conf)
            conf = conf.replace('bend_addr = "127.0.0.1:65443"', 'bend_addr = "127.0.0.1:16444"')
        with open(conf_path, 'w', encoding='utf-8') as f:
            f.write(conf)

    start(PS_DIR, os.path.join(RT, 'seeinps.exe'))
    wait_http(PS1 + '/health')
    start(PS2_DIR, os.path.join(RT, 'seeinps.exe'))
    wait_http(PS2 + '/health')
    time.sleep(3)
    requests.post(f'{PS1}/api/v1/auth/init', json={'username': 'user1', 'authCode': code1, 'password': 'PsAdmin123'}, timeout=5)
    requests.post(f'{PS2}/api/v1/auth/init', json={'username': 'user2', 'authCode': code2, 'password': 'PsAdmin123'}, timeout=5)
    time.sleep(1)
    ph1, ph2 = ps_login(PS1, 'user1'), ps_login(PS2, 'user2')

    print('=== 2. user2 建 2 个代理成功，第 3 个被配额拒绝 ===')
    r = requests.post(f'{PS2}/api/v1/proxies', json={'id': 'q1', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 9001}, headers=ph2, timeout=10)
    assert r.status_code == 200 and r.json().get('data', {}).get('forwardPort'), r.text
    r = requests.post(f'{PS2}/api/v1/proxies', json={'id': 'q2', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 9002}, headers=ph2, timeout=10)
    assert r.status_code == 200, r.text
    r = requests.post(f'{PS2}/api/v1/proxies', json={'id': 'q3', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 9003}, headers=ph2, timeout=10)
    print('  q3 ->', r.status_code, r.text[:120])
    assert r.status_code == 502 and '端口数量已达上限' in r.text, 'expect quota message'
    print('  OK: quota enforced with friendly message')

    print('=== 3. 编辑 user2：不限配额 + 用户池 {20005,20006} -> 收紧踢线重连，端口落用户池 ===')
    r = requests.put(f'{BASE}/api/v1/users/user2', json={'maxPorts': 0, 'portRanges': [{'start': 20005, 'end': 20006}]}, headers=h, timeout=5)
    print('  update ->', r.text[:150])
    assert r.status_code == 200 and r.json().get('data', {}).get('kicked') is True, 'expect kicked=true'
    # 两个代理重连分配顺序存在竞态，断言按端口集合判断（都在用户池内即可）
    assert wait_for(lambda: sorted(p.get('forwardPort') for p in requests.get(f'{PS2}/api/v1/proxies', headers=ph2, timeout=5).json().get('data', [])) == [20005, 20006], 90), 'ports should move into user pool'
    ports = sorted(p.get('forwardPort') for p in requests.get(f'{PS2}/api/v1/proxies', headers=ph2, timeout=5).json().get('data', []))
    print('  q1/q2 ports after re-alloc:', ports)
    r = requests.post(f'{PS2}/api/v1/proxies', json={'id': 'q3', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 9003}, headers=ph2, timeout=10)
    print('  q3 in full user pool ->', r.status_code, r.text[:120])
    assert r.status_code == 502 and '端口池获取失败' in r.text, 'expect user-pool-exhausted message'
    print('  OK: user pool constraint enforced')

    print('=== 4. user1 有效期改为昨天 -> 30s 内断开；改回明天 -> 自动恢复 ===')
    r = requests.post(f'{PS1}/api/v1/proxies', json={'id': 'e1', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 9101}, headers=ph1, timeout=10)
    assert r.status_code == 200, r.text
    yesterday = (datetime.date.today() - datetime.timedelta(days=1)).isoformat()
    tomorrow = (datetime.date.today() + datetime.timedelta(days=1)).isoformat()
    r = requests.put(f'{BASE}/api/v1/users/user1', json={'expireDate': yesterday}, headers=h, timeout=5)
    assert r.status_code == 200, r.text
    assert wait_for(lambda: requests.get(f'{PS1}/api/v1/status', headers=ph1, timeout=3).json().get('data', {}).get('status') == 'disconnected', 60), 'user1 should be kicked within ~30s'
    print('  kicked OK (status=disconnected)')
    r = requests.put(f'{BASE}/api/v1/users/user1', json={'expireDate': tomorrow}, headers=h, timeout=5)
    assert r.status_code == 200, r.text
    assert wait_for(lambda: requests.get(f'{PS1}/api/v1/status', headers=ph1, timeout=3).json().get('data', {}).get('status') == 'connected', 150, desc='reconnect'), 'user1 should reconnect after expiry extended'
    time.sleep(35)  # 等 resync 恢复代理
    ports1 = [p.get('forwardPort') for p in requests.get(f'{PS1}/api/v1/proxies', headers=ph1, timeout=5).json().get('data', [])]
    print('  user1 proxies after recovery:', ports1)
    assert len(ports1) == 1 and ports1[0], 'proxy should be restored after re-enable'
    print('  OK: expiry disconnect + recovery')

    print('=== 5. 用户列表字段 ===')
    r = requests.get(f'{BASE}/api/v1/users', headers=h, timeout=5)
    users = {u['username']: u for u in r.json().get('data', [])}
    print('  user1:', {k: users['user1'].get(k) for k in ['expireDate', 'expired', 'maxPorts', 'portRanges', 'usedPorts']})
    print('  user2:', {k: users['user2'].get(k) for k in ['expireDate', 'expired', 'maxPorts', 'portRanges', 'usedPorts']})
    assert users['user2']['portRanges'] == [{'start': 20005, 'end': 20006}]
    assert users['user2']['usedPorts'] == 2
    assert users['user1']['expired'] is False
    assert users['user1']['expireDate'] == tomorrow
    print('=== DONE ===')


if __name__ == '__main__':
    try:
        main()
    finally:
        for p in procs:
            p.terminate()
        for exe in ['seeinpm.exe', 'seeinps.exe']:
            subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
        sys.stdout.flush()
