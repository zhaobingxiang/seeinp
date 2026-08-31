# -*- coding: utf-8 -*-
"""
端口池耗尽创建代理 e2e：
1. PM 端口池配置为单端口 {20000,20000}，启动 PM/PS 建用户
2. 创建代理 p1 -> 拿到 20000（池满）
3. 创建代理 p2 -> 期望 502 且 message == "端口池获取失败，请联系管理员"
4. 验证 p2 已回滚（列表只有 p1）
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

procs = []
MSG = '端口池获取失败，请联系管理员'


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


def start(workdir, exe):
    logpath = os.path.join(workdir, 'logs', 'seeinpm.log' if 'pm' in workdir else 'seeinps.log')
    f = open(logpath, 'wb', buffering=0)
    p = subprocess.Popen([exe, '-conf', 'conf/seeinpm.toml' if 'pm' in workdir else 'conf/seeinps.toml'],
                         cwd=workdir, stdout=f, stderr=subprocess.STDOUT)
    procs.append(p)
    return p


def wait_http(url, timeout=20):
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            requests.get(url, timeout=2)
            return True
        except Exception:
            time.sleep(0.5)
    return False


def main():
    with open(PM_CONF, 'r', encoding='utf-8') as f:
        orig_conf = f.read()
    try:
        print('=== 0. 端口池改为单端口 {20000,20000} 并启动 ===')
        cleanup()
        for exe in ['seeinpm.exe', 'seeinps.exe']:
            subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
        time.sleep(1)
        patched = re.sub(r'\{ start = 20000, end = \d+ \}', '{ start = 20000, end = 20000 }', orig_conf)
        assert 'end = 20000 }' in patched, 'conf patch failed'
        with open(PM_CONF, 'w', encoding='utf-8') as f:
            f.write(patched)

        start(PM_DIR, os.path.join(RT, 'seeinpm.exe'))
        wait_http(BASE + '/health')
        requests.post(f'{BASE}/api/v1/auth/init', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
        r = requests.post(f'{BASE}/api/v1/auth/login', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
        token = r.json().get('data', {}).get('access_token', '')
        h = {'Authorization': f'Bearer {token}'}
        r = requests.post(f'{BASE}/api/v1/users', json={'username': 'user1', 'remark': 'e2e'}, headers=h, timeout=5)
        auth_code = r.json().get('data', {}).get('authCode', '')
        conf_path = os.path.join(PS_DIR, 'conf', 'seeinps.toml')
        with open(conf_path, 'r', encoding='utf-8') as f:
            conf = f.read()
        conf = re.sub(r'auth_code = ".*"', f'auth_code = "{auth_code}"', conf)
        with open(conf_path, 'w', encoding='utf-8') as f:
            f.write(conf)
        start(PS_DIR, os.path.join(RT, 'seeinps.exe'))
        wait_http(PSB + '/health', timeout=25)
        time.sleep(3)
        requests.post(f'{PSB}/api/v1/auth/init', json={'username': 'user1', 'authCode': auth_code, 'password': 'PsAdmin123'}, timeout=5)
        time.sleep(1)
        r = requests.post(f'{PSB}/api/v1/auth/login', json={'username': 'user1', 'password': 'PsAdmin123'}, timeout=5)
        ph = {'Authorization': 'Bearer ' + r.json().get('data', {}).get('token', '')}

        print('=== 1. 创建 p1，应拿到唯一端口 20000 ===')
        r = requests.post(f'{PSB}/api/v1/proxies', json={'id': 'p1', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 8088}, headers=ph, timeout=10)
        port = r.json().get('data', {}).get('forwardPort')
        print('  p1 forwardPort =', port)
        assert port == 20000, f'p1 should get 20000, got {port}'

        print('=== 2. 池满后创建 p2，应 502 + 友好提示 ===')
        r = requests.post(f'{PSB}/api/v1/proxies', json={'id': 'p2', 'type': 'tcp', 'localAddr': '127.0.0.1', 'localPort': 8089}, headers=ph, timeout=10)
        print('  status =', r.status_code, 'body =', r.text[:200])
        assert r.status_code == 502, f'expected 502, got {r.status_code}'
        assert MSG in r.text, f'expected message {MSG!r} in body'
        print('  OK: pool-exhausted message correct')

        print('=== 3. p2 已回滚 ===')
        r = requests.get(f'{PSB}/api/v1/proxies', headers=ph, timeout=5)
        ids = [p.get('id') for p in r.json().get('data', [])]
        print('  proxies =', ids)
        assert ids == ['p1'], f'p2 should be rolled back, got {ids}'
        print('  OK: rollback works')

        print('=== DONE ===')
    finally:
        with open(PM_CONF, 'w', encoding='utf-8') as f:
            f.write(orig_conf)
        for p in procs:
            p.terminate()
        for exe in ['seeinpm.exe', 'seeinps.exe']:
            subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
        sys.stdout.flush()


if __name__ == '__main__':
    main()
