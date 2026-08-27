# seeinp
seeinp（时遇）是一个轻量级内网穿透与反向代理平台，采用三层架构：seeinpm（中央管理平台）、seeinps（服务端代理，主动拨出无需公网IP）、seeinpc（Windows客户端/VPN）。基于 TLS 长连接 + yamux 多路复用实现控制信令与代理数据共线传输，协议为长度前缀+JSON。前端 Vue 3 + Element Plus，通过 go:embed 嵌入单二进制。支持端口池管理、用户分组、SQLite 存储、版本分发，适用于暴露内部服务进行远程访问与测试。
