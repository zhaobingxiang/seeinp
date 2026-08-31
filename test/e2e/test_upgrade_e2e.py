# -*- coding: utf-8 -*-
"""
版本管理 e2e（Windows 本地，覆盖除 exec 自替换外的完整链路）：
1. 构建注入版本 1.0.26.0829.01，health/clients/status 均可见
2. 上传版本包 1.0.26.0829.02（sha256 服务端计算）-> 列表可见
3. 非法场景：重复版本 409、格式错误 400
4. 节点 upgradable=true -> 一键升级 -> PS 收流校验通过并 UPGRADE_REPORT downloaded（Windows 不重启）
5. 推送相同版本（回滚语义）允许
6. 离线节点升级 502
7. 删除版本 -> 记录与文件同时清除
"""
import hashlib
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
PKG = os.path.join(RT, 'seeinps.exe')

V_OLD = '1.0.26.0829.01'  # 由 health 接口动态覆盖（跟随 build/build.sh 的构建号）
V_NEW = '1.0.26.0829.02'

procs = []


def cleanup():
    ts = time.strftime('%H%M%S')
    for d in [PM_DIR, PS_DIR]:
        datadir = os.path.join(d, 'data')
        os.makedirs(datadir, exist_ok=True)
        for f in os.listdir(datadir):
            p = os.path.join(datadir, f)
            if f.endswith('.db') or f.endswith('.db-wal') or f.endswith('.db-shm'):
                try:
                    os.rename(p, p + f'.bak{ts}')
                except OSError:
                    pass
        shutil.rmtree(os.path.join(PM_DIR, 'data', 'releases'), ignore_errors=True)
        logdir = os.path.join(d, 'logs')
        shutil.rmtree(logdir, ignore_errors=True)
        os.makedirs(logdir, exist_ok=True)


def start(workdir, exe):
    logname = 'seeinpm.log' if 'pm' in workdir else 'seeinps.log'
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


def wait_for(fn, timeout, interval=1):
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            if fn():
                return True
        except Exception:
            pass
        time.sleep(interval)
    return False


def main():
    global V_OLD, V_NEW
    print('=== 0. 启动（版本 %s）===' % V_OLD)
    cleanup()
    for exe in ['seeinpm.exe', 'seeinps.exe']:
        subprocess.run(['taskkill', '/F', '/IM', exe], capture_output=True)
    time.sleep(1)

    start(PM_DIR, os.path.join(RT, 'seeinpm.exe'))
    wait_http(BASE + '/health')
    r = requests.get(BASE + '/health', timeout=5).json()
    print('  health:', r)
    V_OLD = r.get('version')
    seg = V_OLD.split('.')
    seg[-1] = str(int(seg[-1]) + 1)
    V_NEW = '.'.join(seg)
    globals()['V_OLD'], globals()['V_NEW'] = V_OLD, V_NEW
    print('  test versions:', V_OLD, '->', V_NEW)

    requests.post(f'{BASE}/api/v1/auth/init', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
    r = requests.post(f'{BASE}/api/v1/auth/login', json={'username': 'admin', 'password': 'Admin@123'}, timeout=5)
    h = {'Authorization': f'Bearer {r.json().get("data", {}).get("access_token", "")}'}
    r = requests.post(f'{BASE}/api/v1/users', json={'username': 'user1'}, headers=h, timeout=5)
    code1 = r.json().get('data', {}).get('authCode', '')

    conf_path = os.path.join(PS_DIR, 'conf', 'seeinps.toml')
    with open(conf_path, 'r', encoding='utf-8') as f:
        conf = f.read()
    conf = re.sub(r'auth_code = ".*"', f'auth_code = "{code1}"', conf)
    with open(conf_path, 'w', encoding='utf-8') as f:
        f.write(conf)
    start(PS_DIR, os.path.join(RT, 'seeinps.exe'))
    wait_http(PSB + '/health')
    time.sleep(3)
    requests.post(f'{PSB}/api/v1/auth/init', json={'username': 'user1', 'authCode': code1, 'password': 'PsAdmin123'}, timeout=5)
    time.sleep(1)
    r = requests.post(f'{PSB}/api/v1/auth/login', json={'username': 'user1', 'password': 'PsAdmin123'}, timeout=5)
    ph = {'Authorization': 'Bearer ' + r.json().get('data', {}).get('token', '')}
    r = requests.get(f'{PSB}/api/v1/status', headers=ph, timeout=5).json()
    print('  ps status version:', r.get('data', {}).get('version'))
    assert r.get('data', {}).get('version') == V_OLD, 'PS status version mismatch'

    r = requests.get(f'{BASE}/api/v1/clients', headers=h, timeout=5).json()
    clients = {c['username']: c for c in r.get('data', [])}
    print('  clients:', clients)
    assert clients['user1']['version'] == V_OLD, 'client version should come from HELLO'

    print('=== 1. 上传版本包 %s windows/amd64 ===' % V_NEW)
    pkg_sha = hashlib.sha256(open(PKG, 'rb').read()).hexdigest()
    fd = {'endpoint': (None, 'seeinps'), 'version': (None, V_NEW), 'goos': (None, 'windows'), 'goarch': (None, 'amd64'),
          'note': (None, 'e2e upgrade package'), 'file': ('seeinps', open(PKG, 'rb'), 'application/octet-stream')}
    r = requests.post(f'{BASE}/api/v1/versions', files=fd, headers=h, timeout=120)
    print('  upload ->', r.status_code, r.text[:160])
    assert r.status_code == 200 and r.json()['code'] == 0, 'upload failed'
    assert r.json()['data']['sha256'] == pkg_sha, 'server sha256 mismatch'
    vid = r.json()['data']['id']

    r = requests.get(f'{BASE}/api/v1/versions?endpoint=seeinps', headers=h, timeout=5).json()
    assert len(r['data']) == 1 and r['data'][0]['version'] == V_NEW and r['data'][0]['sha256'] == pkg_sha
    assert r['data'][0]['goos'] == 'windows' and r['data'][0]['goarch'] == 'amd64'
    print('  version list OK')

    print('=== 2. 非法场景 ===')
    fd = {'endpoint': (None, 'seeinps'), 'version': (None, V_NEW), 'goos': (None, 'windows'), 'goarch': (None, 'amd64'),
          'note': (None, ''), 'file': ('seeinps', open(PKG, 'rb'), 'application/octet-stream')}
    r = requests.post(f'{BASE}/api/v1/versions', files=fd, headers=h, timeout=120)
    print('  duplicate ->', r.status_code, r.text[:100])
    assert r.status_code == 409, 'expect duplicate 409'
    fd = {'endpoint': (None, 'seeinps'), 'version': (None, '1.0.0'), 'note': (None, ''), 'file': ('seeinps', open(PKG, 'rb'), 'application/octet-stream')}
    r = requests.post(f'{BASE}/api/v1/versions', files=fd, headers=h, timeout=120)
    print('  bad format ->', r.status_code, r.text[:100])
    assert r.status_code == 400, 'expect bad format 400'
    # 同版本号不同平台允许并存
    fd = {'endpoint': (None, 'seeinps'), 'version': (None, V_NEW), 'goos': (None, 'linux'), 'goarch': (None, 'amd64'),
          'note': (None, ''), 'file': ('seeinps', open(PKG, 'rb'), 'application/octet-stream')}
    r = requests.post(f'{BASE}/api/v1/versions', files=fd, headers=h, timeout=120)
    print('  same version other platform ->', r.status_code)
    assert r.status_code == 200, 'same version with other platform should be allowed'
    r = requests.get(f'{BASE}/api/v1/versions?endpoint=seeinps', headers=h, timeout=5).json()
    assert len(r['data']) == 2, 'expect two platform packages'

    print('=== 3. 节点可升级标记 + 平台校验 + 一键升级 ===')
    r = requests.get(f'{BASE}/api/v1/clients', headers=h, timeout=5).json()
    c1 = [c for c in r['data'] if c['username'] == 'user1'][0]
    assert c1['upgradable'] is True and c1['upgrading'] is False
    assert c1['goos'] == 'windows' and c1['goarch'] == 'amd64', 'node platform should come from HELLO'
    # linux 包推给 windows 节点必须被拒
    linux_vid = [v['id'] for v in requests.get(f'{BASE}/api/v1/versions?endpoint=seeinps', headers=h, timeout=5).json()['data'] if v['goos'] == 'linux'][0]
    r = requests.post(f'{BASE}/api/v1/clients/user1/upgrade', json={'versionId': linux_vid}, headers=h, timeout=30)
    print('  cross-platform upgrade ->', r.status_code, r.text[:140])
    assert r.status_code == 400 and '平台不匹配' in r.text, 'expect platform mismatch 400'
    r = requests.post(f'{BASE}/api/v1/clients/user1/upgrade', json={'versionId': vid}, headers=h, timeout=60)
    print('  upgrade ->', r.status_code, r.text[:160])
    assert r.status_code == 200 and r.json()['code'] == 0, 'upgrade request failed'

    pmlog = open(os.path.join(PM_DIR, 'logs', 'seeinpm.log'), 'rb').read().decode(errors='replace')
    ok = wait_for(lambda: 'package %s verified' % V_NEW in open(os.path.join(PM_DIR, 'logs', 'seeinpm.log'), 'rb').read().decode(errors='replace'), 20)
    assert ok, 'PM should receive UPGRADE_REPORT downloaded'
    print('  UPGRADE_REPORT received by PM')

    pslog_path = os.path.join(PS_DIR, 'logs', 'seeinps.log')
    ok = wait_for(lambda: 'windows dev: restart skipped' in open(pslog_path, 'rb').read().decode(errors='replace'), 15)
    assert ok, 'PS should verify and skip restart on windows'
    pslog = open(pslog_path, 'rb').read().decode(errors='replace')
    print('  PS verified package (windows: no restart) OK')
    ok = wait_for(lambda: not os.path.exists(os.path.join(PS_DIR, 'data', 'seeinps.upgrade.tmp')), 10)
    assert ok, 'tmp file should be cleaned'
    r = requests.get(f'{BASE}/api/v1/clients', headers=h, timeout=5).json()
    c1 = [c for c in r['data'] if c['username'] == 'user1'][0]
    assert c1['upgrading'] is False and c1['version'] == V_OLD, 'no restart on windows, version unchanged'

    print('=== 4. 推送相同版本（回滚语义）允许 ===')
    r = requests.post(f'{BASE}/api/v1/clients/user1/upgrade', json={'versionId': vid}, headers=h, timeout=60)
    assert r.status_code == 200, r.text
    print('  OK')

    print('=== 5. 离线节点升级 502 ===')
    requests.post(f'{BASE}/api/v1/users', json={'username': 'ghost'}, headers=h, timeout=5)
    r = requests.post(f'{BASE}/api/v1/clients/ghost/upgrade', json={'versionId': vid}, headers=h, timeout=15)
    print('  offline ->', r.status_code, r.text[:120])
    assert r.status_code == 502, 'expect offline 502'

    print('=== 6. 删除版本（windows 包）===')
    r = requests.delete(f'{BASE}/api/v1/versions/{vid}', headers=h, timeout=5)
    assert r.status_code == 200, r.text
    r = requests.get(f'{BASE}/api/v1/versions?endpoint=seeinps', headers=h, timeout=5).json()
    assert len(r['data']) == 1 and r['data'][0]['goos'] == 'linux', 'only linux package should remain'
    assert not os.path.exists(os.path.join(PM_DIR, 'data', 'releases', 'seeinps', V_NEW, 'windows-amd64')), 'windows package dir should be removed'
    assert os.path.exists(os.path.join(PM_DIR, 'data', 'releases', 'seeinps', V_NEW, 'linux-amd64')), 'linux package dir should remain'
    print('  OK')

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
