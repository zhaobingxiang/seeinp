# -*- coding: utf-8 -*-
"""
B 端自主升级 e2e（Windows 本地）：
1. PS B 端 /pm-versions 初始为空 -> PM 上传包后能查到（控制通道 VERSION_LIST）
2. /pm-upgrade 拉取升级 -> 状态流转 pulling/receiving(字节进度) -> applying（Windows 不重启）
3. 非法版本号 400；拉取不存在的版本 -> failed（无此版本或平台不匹配）
4. 自上传升级 /self-upgrade -> applying
"""
import hashlib
import os
import re
import shutil
import subprocess
import time

import requests

BASE = 'http://127.0.0.1:9998'
PSB = 'http://127.0.0.1:65443'
RT = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'runtime')
PKG = os.path.join(RT, 'seeinps.exe')


def wait_http(url, timeout=25):
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            requests.get(url, timeout=2)
            return True
        except Exception:
            time.sleep(0.5)
    return False


def wait_for(fn, timeout=30, interval=1):
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            if fn():
                return True
        except Exception:
            pass
        time.sleep(interval)
    return False


def fresh_start():
    for exe in ['seeinpm.exe', 'seeinps.exe']:
        subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
    # 等进程真正退出（taskkill 异步，句柄未释放会挡住 DB 改名）
    for _ in range(10):
        out = subprocess.run(['tasklist', '/FI', 'IMAGENAME eq seeinpm.exe'], capture_output=True).stdout.decode('gbk', errors='ignore')
        out2 = subprocess.run(['tasklist', '/FI', 'IMAGENAME eq seeinps.exe'], capture_output=True).stdout.decode('gbk', errors='ignore')
        if 'seeinpm.exe' not in out and 'seeinps.exe' not in out2:
            break
        time.sleep(1)
    for d in ['pm', 'ps']:
        for f in os.listdir(f'{RT}/{d}/data'):
            if f.startswith('seeinpm.db') or f.startswith('seeinps.db'):
                p = f'{RT}/{d}/data/{f}'
                for i in range(15):
                    try:
                        os.remove(p)
                        break
                    except PermissionError:
                        raise RuntimeError(f'{p} 被占用：上一轮进程未退出，请检查')
                    except FileNotFoundError:
                        break
        shutil.rmtree(f'{RT}/{d}/logs', ignore_errors=True)
        os.makedirs(f'{RT}/{d}/logs', exist_ok=True)
    pm = subprocess.Popen([f'{RT}/seeinpm.exe', '-conf', 'conf/seeinpm.toml'], cwd=f'{RT}/pm',
                          stdout=open(f'{RT}/pm/logs/seeinpm.log', 'wb'), stderr=subprocess.STDOUT)
    wait_http(BASE + '/health')
    requests.post(f'{BASE}/api/v1/auth/init', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
    r = requests.post(f'{BASE}/api/v1/auth/login', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
    h = {'Authorization': f'Bearer {r.json()["data"]["access_token"]}'}
    r = requests.post(f'{BASE}/api/v1/users', json={'username': 'user1'}, headers=h, timeout=5)
    if r.status_code != 200 or 'authCode' not in r.text:
        raise RuntimeError(f'create user failed: {r.status_code} {r.text[:200]}')
    code = r.json()['data']['authCode']
    conf = open(f'{RT}/ps/conf/seeinps.toml', encoding='utf-8').read()
    open(f'{RT}/ps/conf/seeinps.toml', 'w', encoding='utf-8').write(re.sub(r'auth_code = ".*"', f'auth_code = "{code}"', conf))
    ps = subprocess.Popen([f'{RT}/seeinps.exe', '-conf', 'conf/seeinps.toml'], cwd=f'{RT}/ps',
                          stdout=open(f'{RT}/ps/logs/seeinps.log', 'wb'), stderr=subprocess.STDOUT)
    wait_http(PSB + '/health')
    time.sleep(4)
    requests.post(f'{PSB}/api/v1/auth/init', json={'username': 'user1', 'authCode': code, 'password': 'PsAdmin123'}, timeout=5)
    time.sleep(1)
    r = requests.post(f'{PSB}/api/v1/auth/login', json={'username': 'user1', 'password': 'PsAdmin123'}, timeout=5)
    ph = {'Authorization': 'Bearer ' + r.json()['data']['token']}
    return pm, ps, h, ph


def main():
    print('=== 0. 启动 ===')
    pm, ps, h, ph = fresh_start()

    print('=== 1. PM 无版本时 B 端列表为空 ===')
    r = requests.get(f'{PSB}/api/v1/pm-versions', headers=ph, timeout=20)
    print('  pm-versions:', r.json())
    assert r.status_code == 200 and r.json()['data'] == [], 'expect empty list'

    print('=== 2. PM 上传版本 1.0.26.0830.01（windows/amd64）===')
    fd = {'endpoint': (None, 'seeinps'), 'version': (None, '1.0.26.0830.01'), 'goos': (None, 'windows'),
          'goarch': (None, 'amd64'), 'note': (None, 'e2e'), 'file': ('seeinps', open(PKG, 'rb'), 'application/octet-stream')}
    r = requests.post(f'{BASE}/api/v1/versions', files=fd, headers=h, timeout=120)
    assert r.status_code == 200, r.text

    print('=== 3. B 端列表可见（按平台过滤）===')
    r = requests.get(f'{PSB}/api/v1/pm-versions', headers=ph, timeout=20).json()
    print('  items:', r['data'])
    assert any(v['version'] == '1.0.26.0830.01' for v in r['data']), 'version should be listed'

    print('=== 4. 拉取升级 -> 状态流转到 applying ===')
    r = requests.post(f'{PSB}/api/v1/pm-upgrade', json={'version': '1.0.26.0830.01'}, headers=ph, timeout=30)
    print('  pm-upgrade:', r.json())
    assert r.status_code == 200 and r.json()['code'] == 0
    saw_receiving = wait_for(lambda: requests.get(f'{PSB}/api/v1/self-upgrade/status', headers=ph, timeout=5).json()['data']['stage'] in ('receiving', 'applying'), 15)
    print('  saw receiving/applying:', saw_receiving)
    assert saw_receiving, 'should observe receiving stage'
    assert wait_for(lambda: requests.get(f'{PSB}/api/v1/self-upgrade/status', headers=ph, timeout=5).json()['data']['stage'] == 'applying', 20), 'should reach applying'
    st = requests.get(f'{PSB}/api/v1/self-upgrade/status', headers=ph, timeout=5).json()['data']
    print('  status:', st)
    assert st['doneBytes'] == st['totalBytes'] and st['totalBytes'] > 0, 'bytes progress expected'
    assert not os.path.exists(os.path.join(RT, 'ps', 'data', 'seeinps.upgrade.tmp')), 'tmp cleaned'
    print('  OK')

    print('=== 4.5 重启 PS 清除 applying 驻留态（Windows 开发平台特性）===')
    ps.terminate()
    subprocess.run(['taskkill', '/F', '/IM', 'seeinps.exe'], capture_output=True)
    time.sleep(1)
    ps = subprocess.Popen([f'{RT}/seeinps.exe', '-conf', 'conf/seeinps.toml'], cwd=f'{RT}/ps',
                          stdout=open(f'{RT}/ps/logs/seeinps.log', 'ab'), stderr=subprocess.STDOUT)
    wait_http(PSB + '/health')
    time.sleep(3)
    r = requests.post(f'{PSB}/api/v1/auth/login', json={'username': 'user1', 'password': 'PsAdmin123'}, timeout=5)
    ph = {'Authorization': 'Bearer ' + r.json()['data']['token']}

    print('=== 5. 拉取不存在的版本 -> failed ===')
    r = requests.post(f'{PSB}/api/v1/pm-upgrade', json={'version': '9.9.99.9999.99'}, headers=ph, timeout=30)
    assert r.status_code == 200
    assert wait_for(lambda: requests.get(f'{PSB}/api/v1/self-upgrade/status', headers=ph, timeout=5).json()['data']['stage'] == 'failed', 15)
    st = requests.get(f'{PSB}/api/v1/self-upgrade/status', headers=ph, timeout=5).json()['data']
    print('  failed reason:', st['error'])
    assert '无此版本' in st['error'] or '平台不匹配' in st['error']
    print('  OK')

    print('=== 6. 自上传升级 ===')
    fd = {'version': (None, '1.0.26.0830.02'), 'file': ('seeinps', open(PKG, 'rb'), 'application/octet-stream')}
    r = requests.post(f'{PSB}/api/v1/self-upgrade', files=fd, headers=ph, timeout=120)
    print('  self-upgrade:', r.json())
    assert r.status_code == 200 and r.json()['code'] == 0
    # Windows 开发平台：applying 瞬间转为 failed 终态（提示仅校验未重启）；Linux 上则 exec 重启
    assert wait_for(lambda: requests.get(f'{PSB}/api/v1/self-upgrade/status', headers=ph, timeout=5).json()['data']['stage'] in ('applying', 'failed'), 15)
    st = requests.get(f'{PSB}/api/v1/self-upgrade/status', headers=ph, timeout=5).json()['data']
    print('  self-upgrade status:', st)
    assert st['stage'] == 'failed' and 'Windows' in st['error'], 'windows dev terminal expected'
    print('  OK')

    print('=== 7. 非法版本号 400 ===')
    fd = {'version': (None, 'bad'), 'file': ('seeinps', open(PKG, 'rb'), 'application/octet-stream')}
    r = requests.post(f'{PSB}/api/v1/self-upgrade', files=fd, headers=ph, timeout=60)
    print('  bad version ->', r.status_code, r.text[:100])
    assert r.status_code == 400
    print('  OK')

    print('=== DONE ===')
    try:
        pm.terminate(); ps.terminate()
    except Exception:
        pass
    subprocess.run(['taskkill', '/F', '/IM', 'seeinpm.exe'], capture_output=True)
    subprocess.run(['taskkill', '/F', '/IM', 'seeinps.exe'], capture_output=True)


if __name__ == '__main__':
    main()
