package workstationlan

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
)

var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

type link struct {
	nic    net.Interface
	ip     net.IP
	subnet *net.IPNet
}

func links() []link {
	var out []link
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ip, mask, e := net.ParseCIDR(a.String())
			if e != nil || ip.To4() == nil || !ip.IsPrivate() {
				continue
			}
			out = append(out, link{i, ip.To4(), mask})
		}
	}
	return out
}
func onLAN(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	for _, l := range links() {
		if l.subnet.Contains(ip) {
			return true
		}
	}
	return false
}
func addressOK(addr string) bool {
	h, p, e := net.SplitHostPort(addr)
	if e != nil || p != strconv.Itoa(ServicePort) {
		return false
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.To4() != nil && onLAN(ip)
}
func header(name string, t uint16) dns.RR_Header {
	return dns.RR_Header{Name: name, Rrtype: t, Class: dns.ClassINET, Ttl: 30}
}
func announce(l link, self Peer, port int, q *dns.Msg) *dns.Msg {
	suffix := "cage-" + self.ID[:16]
	instance := suffix + "." + serviceName
	host := suffix + ".local."
	m := new(dns.Msg)
	m.SetReply(q)
	m.Authoritative = true
	m.RecursionAvailable = false
	m.Answer = []dns.RR{&dns.PTR{Hdr: header(serviceName, dns.TypePTR), Ptr: instance}}
	m.Extra = []dns.RR{&dns.SRV{Hdr: header(instance, dns.TypeSRV), Port: uint16(port), Target: host}, &dns.TXT{Hdr: header(instance, dns.TypeTXT), Txt: []string{"v=1", "key=" + self.Public, "name=" + self.Name}}, &dns.A{Hdr: header(host, dns.TypeA), A: l.ip}}
	return m
}

// DNS-SD service queries from application clients use an ephemeral source port and receive a legacy unicast mDNS reply.
func responder(ctx context.Context, l link, self Peer, port int) (*net.UDPConn, error) {
	c, e := net.ListenMulticastUDP("udp4", &l.nic, mdnsGroup)
	if e != nil {
		return nil, e
	}
	pc := ipv4.NewPacketConn(c)
	_ = pc.SetMulticastInterface(&l.nic)
	_ = pc.SetMulticastTTL(255)
	// Winsock applies multicast loopback on the receiving socket. Go's listener disables it by default.
	_ = pc.SetMulticastLoopback(true)
	go func() {
		stop := context.AfterFunc(ctx, func() { c.Close() })
		defer stop()
		b := make([]byte, 1500)
		for {
			n, src, e := c.ReadFromUDP(b)
			if e != nil {
				return
			}
			if !l.subnet.Contains(src.IP) {
				continue
			}
			var q dns.Msg
			if q.Unpack(b[:n]) != nil || q.Response {
				continue
			}
			for _, question := range q.Question {
				if !strings.EqualFold(question.Name, serviceName) || question.Qtype != dns.TypePTR {
					continue
				}
				reply := announce(l, self, port, &q)
				dst := src
				if src.Port == 5353 && question.Qclass&0x8000 == 0 {
					dst = mdnsGroup
					reply.Id = 0
					reply.Question = nil
				}
				data, e := reply.Pack()
				if e == nil {
					_, _ = c.WriteToUDP(data, dst)
				}
				break
			}
		}
	}()
	return c, nil
}
func advertised(m *dns.Msg, src net.IP) (Peer, bool) {
	if !m.Response {
		return Peer{}, false
	}
	var ptr string
	records := append(append([]dns.RR{}, m.Answer...), m.Extra...)
	for _, rr := range records {
		if p, ok := rr.(*dns.PTR); ok && strings.EqualFold(p.Hdr.Name, serviceName) {
			ptr = p.Ptr
			break
		}
	}
	if ptr == "" {
		return Peer{}, false
	}
	p := Peer{}
	var port uint16
	var version string
	for _, rr := range records {
		if !strings.EqualFold(rr.Header().Name, ptr) {
			continue
		}
		switch v := rr.(type) {
		case *dns.SRV:
			port = v.Port
		case *dns.TXT:
			for _, txt := range v.Txt {
				k, val, _ := strings.Cut(txt, "=")
				switch k {
				case "v":
					version = val
				case "key":
					p.Public = val
				case "name":
					p.Name = cleanName(val)
				}
			}
		}
	}
	k, e := decodeKey(p.Public)
	if e != nil || version != "1" || int(port) != ServicePort {
		return Peer{}, false
	}
	p.ID = idFor(k)
	p.Address = net.JoinHostPort(src.String(), fmt.Sprint(port))
	return p, true
}
func discover(ctx context.Context) ([]Peer, error) {
	active := links()
	if len(active) == 0 {
		return nil, fmt.Errorf("暂时没有可用于局域网发现的 IPv4 接口")
	}
	peers := map[string]Peer{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, l := range active {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := net.ListenUDP("udp4", &net.UDPAddr{IP: l.ip})
			if e != nil {
				return
			}
			defer c.Close()
			pc := ipv4.NewPacketConn(c)
			_ = pc.SetMulticastInterface(&l.nic)
			_ = pc.SetMulticastTTL(255)
			_ = pc.SetMulticastLoopback(true)
			stop := context.AfterFunc(ctx, func() { c.Close() })
			defer stop()
			var q dns.Msg
			q.SetQuestion(serviceName, dns.TypePTR)
			q.RecursionDesired = false
			q.Question[0].Qclass |= 0x8000
			data, _ := q.Pack()
			b := make([]byte, 4096)
			for round := 0; round < 2; round++ {
				if ctx.Err() != nil {
					return
				}
				_, _ = c.WriteToUDP(data, mdnsGroup)
				_ = c.SetReadDeadline(time.Now().Add(600 * time.Millisecond))
				for {
					n, src, e := c.ReadFromUDP(b)
					if e != nil {
						break
					}
					if !l.subnet.Contains(src.IP) {
						continue
					}
					var m dns.Msg
					if m.Unpack(b[:n]) != nil {
						continue
					}
					p, ok := advertised(&m, src.IP)
					if ok {
						mu.Lock()
						peers[p.ID] = p
						mu.Unlock()
					}
				}
			}
		}()
	}
	wg.Wait()
	out := make([]Peer, 0, len(peers))
	for _, v := range peers {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, ctx.Err()
}
