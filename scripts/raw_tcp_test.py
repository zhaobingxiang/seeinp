# -*- coding: utf-8 -*-
"""本机裸TCP测试: 直连20003发最小代理请求, 观察是否被中途RST"""
import socket
import time

req = b"GET http://192.168.0.70/ HTTP/1.1\r\nHost: 192.168.0.70\r\nProxy-Authorization: Basic dGVzdDpUZXN0QHNlZWlucDIwMjY=\r\n\r\n"

print("=== 测试1: 裸TCP到 170.106.109.105:20003 发送代理请求 ===")
try:
    s = socket.create_connection(("170.106.109.105", 20003), timeout=10)
    print("TCP连接成功:", s.getpeername())
    s.sendall(req)
    print("请求已发送(%d字节), 等待响应..." % len(req))
    s.settimeout(12)
    data = b""
    try:
        while True:
            chunk = s.recv(4096)
            if not chunk:
                print("连接被对端关闭(EOF), 已收%d字节" % len(data))
                break
            data += chunk
            if len(data) > 8192:
                break
    except socket.timeout:
        print("等待响应超时(12s), 已收%d字节" % len(data))
    except ConnectionResetError as e:
        print("连接被RST重置! 已收%d字节" % len(data))
    if data:
        print("响应前300字节:", data[:300])
except Exception as e:
    print("失败:", type(e).__name__, e)

print()
print("=== 测试2: 对照组 裸TCP到 170.106.109.105:9998 发送普通GET ===")
try:
    s2 = socket.create_connection(("170.106.109.105", 9998), timeout=10)
    print("TCP连接成功")
    s2.sendall(b"GET /health HTTP/1.1\r\nHost: 170.106.109.105:9998\r\nConnection: close\r\n\r\n")
    s2.settimeout(12)
    data2 = b""
    try:
        while True:
            chunk = s2.recv(4096)
            if not chunk:
                break
            data2 += chunk
    except socket.timeout:
        pass
    print("已收%d字节" % len(data2))
    print("响应前200字节:", data2[:200])
except Exception as e:
    print("失败:", type(e).__name__, e)
