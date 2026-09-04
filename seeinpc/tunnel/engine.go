package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/gvisor/pkg/buffer"
	"github.com/sagernet/gvisor/pkg/tcpip"
	"github.com/sagernet/gvisor/pkg/tcpip/adapters/gonet"
	"github.com/sagernet/gvisor/pkg/tcpip/header"
	"github.com/sagernet/gvisor/pkg/tcpip/link/channel"
	"github.com/sagernet/gvisor/pkg/tcpip/network/ipv4"
	"github.com/sagernet/gvisor/pkg/tcpip/stack"
	"github.com/sagernet/gvisor/pkg/tcpip/transport/icmp"
	"github.com/sagernet/gvisor/pkg/tcpip/transport/tcp"
	"github.com/sagernet/gvisor/pkg/tcpip/transport/udp"
	"github.com/sagernet/gvisor/pkg/waiter"
	wtun "golang.zx2c4.com/wireguard/tun"
)

const (
	nicID         = 1
	maxInFlight   = 1024
	dialTimeout   = 15 * time.Second
	outQueueSize  = 1024
	readBatchSize = 8
)

// LogFunc 日志回调（由上层注入，遵循 logx 规范）
type LogFunc func(format string, args ...any)

// Engine 隧道引擎：wintun 设备 ↔ gVisor netstack，TCP 流量经 dial 转发到上游代理。
// 说明：
//   - UDP 无法通过 HTTP 代理传输，netstack 对内无监听端口的 UDP 回送 ICMP 不可达，
//     应用会自动回退（如 DNS 走 TCP）；
//   - ICMP 不回显远端（HTTP 代理无法承载），探测请使用 TCP。
type Engine struct {
	s       *stack.Stack
	ep      *channel.Endpoint
	dev     wtun.Device
	dial    func(ctx context.Context, target string) (net.Conn, error)
	logf    LogFunc
	mtu     uint32
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	conns   atomic.Int64
	stopped atomic.Bool
}

// StartEngine 在给定 wintun 设备上启动协议栈引擎。
// dev 的生命周期由 Engine 接管（Stop 时关闭）。
func StartEngine(dev wtun.Device, mtu uint32, dial func(ctx context.Context, target string) (net.Conn, error), logf LogFunc) (*Engine, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4},
	})
	ep := channel.New(outQueueSize, mtu, "")
	if tcpipErr := s.CreateNIC(nicID, ep); tcpipErr != nil {
		return nil, fmt.Errorf("创建 netstack NIC 失败: %v", tcpipErr)
	}
	// 混杂模式：让目的地址非本机的报文也投递到协议栈（为任意目标建临时地址端点），
	// 从而交给 TCP forwarder 处理；欺骗：允许用任意源地址（内网目标地址）回包。
	// 二者缺失会导致 SYN 被 ipv4 层丢弃，表现为"连接成功但流量不通"。
	if tcpipErr := s.SetPromiscuousMode(nicID, true); tcpipErr != nil {
		return nil, fmt.Errorf("设置混杂模式失败: %v", tcpipErr)
	}
	if tcpipErr := s.SetSpoofing(nicID, true); tcpipErr != nil {
		return nil, fmt.Errorf("设置欺骗模式失败: %v", tcpipErr)
	}
	// 默认路由：所有出站报文经由该 NIC（L3 点对点，无 ARP）
	zero := tcpip.AddressWithPrefix{Address: tcpip.AddrFrom4([4]byte{}), PrefixLen: 0}
	s.SetRouteTable([]tcpip.Route{{Destination: zero.Subnet(), NIC: nicID}})

	e := &Engine{s: s, ep: ep, dev: dev, dial: dial, logf: logf, mtu: mtu}
	fwd := tcp.NewForwarder(s, 0, maxInFlight, e.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)

	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.wg.Add(2)
	go e.readLoop(ctx)
	go e.writeLoop(ctx)
	logf("[TUNNEL] engine started adapter=%s mtu=%d icmp=dropped (no fake echo reply)", AdapterName, mtu)
	return e, nil
}

// Conns 当前转发中的 TCP 连接数
func (e *Engine) Conns() int64 { return e.conns.Load() }

// Stop 停止引擎：取消读写循环、关闭设备与协议栈。
func (e *Engine) Stop() {
	if !e.stopped.CompareAndSwap(false, true) {
		return
	}
	e.cancel()
	_ = e.dev.Close() // 使阻塞中的 Read 返回错误
	e.wg.Wait()
	e.s.Close()
	e.ep.Close()
	e.logf("[TUNNEL] engine stopped conns_left=%d", e.conns.Load())
}

// handleTCP 处理入站 TCP SYN：目标 = 报文的"目的地址:端口"（即内网目标），
// 先完成握手再拨上游代理，失败则关闭连接。
func (e *Engine) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	target := net.JoinHostPort(id.LocalAddress.String(), strconv.Itoa(int(id.LocalPort)))

	var wq waiter.Queue
	ep, tcpipErr := r.CreateEndpoint(&wq)
	if tcpipErr != nil {
		r.Complete(true)
		e.logf("[TUNNEL] create endpoint fail target=%s err=%v", target, tcpipErr)
		return
	}
	r.Complete(false)
	// 开启 keepalive，及时回收死连接
	ep.SocketOptions().SetKeepAlive(true)

	conn := gonet.NewTCPConn(&wq, ep)
	e.conns.Add(1)
	go func() {
		defer e.conns.Add(-1)
		defer conn.Close()

		ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		upstream, err := e.dial(ctx, target)
		cancel()
		if err != nil {
			e.logf("[TUNNEL] dial fail target=%s err=%v", target, err)
			return
		}
		defer upstream.Close()
		e.relay(conn, upstream)
	}()
}

// relay 双向拷贝，任一侧结束即整体结束（defer 关闭两端）。
func (e *Engine) relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}

// readLoop wintun → netstack：读取 IP 报文并注入协议栈。
func (e *Engine) readLoop(ctx context.Context) {
	defer e.wg.Done()
	bufs := make([][]byte, readBatchSize)
	for i := range bufs {
		bufs[i] = make([]byte, e.mtu+4)
	}
	sizes := make([]int, readBatchSize)
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := e.dev.Read(bufs, sizes, 0)
		if n == 0 {
			if err != nil {
				if ctx.Err() == nil {
					e.logf("[TUNNEL] device read error: %v", err)
				}
				return
			}
			continue
		}
		for i := 0; i < n; i++ {
			data := bufs[i][:sizes[i]]
			if len(data) < 20 || data[0]>>4 != 4 {
				continue // 仅处理 IPv4
			}
			if data[9] == 1 { // IPv4 协议字段 = 1 即 ICMPv4
				// 有意丢弃进入的 ICMP：netstack 会为任意目的地址伪造 Echo Reply，
				// 造成 ping "假通"。关闭伪造后 ping 真实超时，连通性请以 TCP 探测为准。
				continue
			}
			cp := make([]byte, len(data))
			copy(cp, data)
			pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(cp)})
			e.ep.InjectInbound(header.IPv4ProtocolNumber, pkt)
			// pkt 所有权随 InjectInbound 移交，不再 DecRef
		}
	}
}

// writeLoop netstack → wintun：协议栈出站报文写回虚拟网卡。
func (e *Engine) writeLoop(ctx context.Context) {
	defer e.wg.Done()
	for {
		pkt := e.ep.ReadContext(ctx)
		if pkt == nil {
			return
		}
		view := pkt.ToView()
		_, err := e.dev.Write([][]byte{view.AsSlice()}, 0)
		view.Release()
		pkt.DecRef()
		if err != nil && ctx.Err() == nil {
			e.logf("[TUNNEL] device write error: %v", err)
			return
		}
	}
}
