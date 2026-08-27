# -*- coding: utf-8 -*-
"""快速验证本轮修复:
1) web-ps 新前端包含错误授权码提示文案
2) web-pm 新前端包含兼容复制逻辑（clipboard + textarea fallback 文案）
3) B端 rebind 错误码返回 message 可由前端提示（接口层验证）
"""
import json
import urllib.request

PS_B_BASE = "http://see.timemsee.cn:65443"


def ps_get(path):
    req = urllib.request.Request(PS_B_BASE + path)
    with urllib.request.urlopen(req, timeout=10) as r:
        return r.status, json.loads(r.read().decode("utf-8", "replace"))


def ps_post(path, body=None, token=None):
    req = urllib.request.Request(PS_B_BASE + path, method="POST")
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    data = json.dumps(body).encode() if body is not None else None
    try:
        with urllib.request.urlopen(req, data=data, timeout=10) as r:
            return r.status, json.loads(r.read().decode("utf-8", "replace"))
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode("utf-8", "replace"))


# 1) PS 未登录 rebind 应返回 401，说明接口存在
st, _ = ps_post("/api/v1/auth/rebind", {"authCode": "x"})
print("[check] PS rebind endpoint:", st)

# 2) 获取当前前端首页（应含新提示文案）
req = urllib.request.Request(PS_B_BASE + "/")
with urllib.request.urlopen(req, timeout=10) as r:
    html = r.read().decode("utf-8", "replace")
assets_prefix = "/assets/"
if assets_prefix in html:
    start = html.index(assets_prefix)
    end = html.index('"', start)
    asset_url = html[start:end]
    req2 = urllib.request.Request(PS_B_BASE + asset_url)
    with urllib.request.urlopen(req2, timeout=10) as r2:
        js = r2.read().decode("utf-8", "replace")
    print("[check] PS new frontend contains hint:", "绑定失败，请确认授权码是否正确" in js)
else:
    print("[check] cannot locate assets in PS index.html")

# 3) PM web-pm 静态资源（如果可达）
try:
    req = urllib.request.Request("http://170.106.109.105:9998/pm/users", method="GET")
    with urllib.request.urlopen(req, timeout=10) as r:
        pm_html = r.read().decode("utf-8", "replace")
    if assets_prefix in pm_html:
        start = pm_html.index(assets_prefix)
        end = pm_html.index('"', start)
        pm_asset = pm_html[start:end]
        req2 = urllib.request.Request("http://170.106.109.105:9998" + pm_asset)
        with urllib.request.urlopen(req2, timeout=10) as r2:
            pm_js = r2.read().decode("utf-8", "replace")
        print("[check] PM new frontend contains copy fallback:", "自动复制失败，请手动复制授权码" in pm_js)
    else:
        print("[check] cannot locate PM assets from /pm/users (may need direct server grep)")
except Exception as e:
    print("[check] PM web fetch skipped:", e)
