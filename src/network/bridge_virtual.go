package network

import (
	"fmt"
	"github.com/danieldin95/openlan-go/src/libol"
	"sync"
	"time"
)

type Learner struct {
	Dest    []byte
	Device  Taper
	Uptime  int64
	NewTime int64
}

type VirtualBridge struct {
	ifMtu    int
	name     string
	lock     sync.RWMutex
	devices  map[string]Taper
	learners map[string]*Learner
	done     chan bool
	ticker   *time.Ticker
	timeout  int
	address  string
	kernel   Taper
	out      *libol.SubLogger
}

func NewVirtualBridge(name string, mtu int) *VirtualBridge {
	b := &VirtualBridge{
		name:     name,
		ifMtu:    mtu,
		devices:  make(map[string]Taper, 1024),
		learners: make(map[string]*Learner, 1024),
		done:     make(chan bool),
		ticker:   time.NewTicker(5 * time.Second),
		timeout:  5 * 60,
		out:      libol.NewSubLogger(name),
	}
	return b
}

func (b *VirtualBridge) Open(addr string) {
	b.out.Info("VirtualBridge.Open %s", addr)
	if addr != "" {
		tap, err := NewKernelTap("default", TapConfig{Type: TAP})
		if err != nil {
			b.out.Error("VirtualBridge.Open new kernel %s", err)
		} else {
			out, err := libol.IpLinkUp(tap.Name())
			if err != nil {
				b.out.Error("VirtualBridge.Open IpAddr %s:%s", err, out)
			}
			b.address = addr
			b.kernel = tap
			out, err = libol.IpAddrAdd(b.kernel.Name(), b.address)
			if err != nil {
				b.out.Error("VirtualBridge.Open IpAddr %s:%s", err, out)
			}
			b.out.Info("VirtualBridge.Open %s", tap.Name())
			_ = b.AddSlave(tap.name)
		}
	} else {
		b.out.Warn("VirtualBridge.Open notSupport address")
	}
	libol.Go(b.Start)
}

func (b *VirtualBridge) Close() error {
	if b.kernel != nil {
		out, err := libol.IpAddrDel(b.kernel.Name(), b.address)
		if err != nil {
			b.out.Error("VirtualBridge.Close: IpAddr %s:%s", err, out)
		}
	}
	b.ticker.Stop()
	b.done <- true
	return nil
}

func (b *VirtualBridge) AddSlave(name string) error {
	b.lock.Lock()
	defer b.lock.Unlock()
	tap := Tapers.Get(name)
	if tap == nil {
		return libol.NewErr("%s notFound", name)
	}
	b.devices[name] = tap
	b.out.Info("VirtualBridge.AddSlave: %s", name)
	libol.Go(func() {
		for {
			data := make([]byte, b.ifMtu)
			n, err := tap.Recv(data)
			if err != nil || n == 0 {
				break
			}
			if libol.HasLog(libol.DEBUG) {
				libol.Debug("VirtualBridge.KernelTap: %s % x", tap.Name(), data[:20])
			}
			m := &Framer{Data: data[:n], Source: tap}
			_ = b.Input(m)
		}
	})
	return nil
}

func (b *VirtualBridge) DelSlave(name string) error {
	b.lock.Lock()
	defer b.lock.Unlock()
	if _, ok := b.devices[name]; ok {
		delete(b.devices, name)
	}
	b.out.Info("VirtualBridge.DelSlave: %s", name)
	return nil
}

func (b *VirtualBridge) Type() string {
	return "virtual"
}

func (b *VirtualBridge) Name() string {
	return b.name
}

func (b *VirtualBridge) SetName(value string) {
	b.name = value
}

func (b *VirtualBridge) SetTimeout(value int) {
	b.timeout = value
}

func (b *VirtualBridge) Forward(m *Framer) error {
	if is := b.Unicast(m); !is {
		_ = b.Flood(m)
	}
	return nil
}

func (b *VirtualBridge) Expire() error {
	deletes := make([]string, 0, 1024)

	//collect need deleted.
	b.lock.RLock()
	for index, learn := range b.learners {
		now := time.Now().Unix()
		if now-learn.Uptime > int64(b.timeout) {
			deletes = append(deletes, index)
		}
	}
	b.lock.RUnlock()

	b.out.Debug("VirtualBridge.Expire delete %d", len(deletes))
	//execute delete.
	b.lock.Lock()
	for _, d := range deletes {
		if _, ok := b.learners[d]; ok {
			delete(b.learners, d)
			b.out.Info("VirtualBridge.Expire: delete %s", d)
		}
	}
	b.lock.Unlock()

	return nil
}

func (b *VirtualBridge) Start() {
	libol.Go(func() {
		for {
			select {
			case <-b.done:
				return
			case t := <-b.ticker.C:
				b.out.Log("VirtualBridge.Start: Tick at %s", t)
				_ = b.Expire()
			}
		}
	})
}

func (b *VirtualBridge) Input(m *Framer) error {
	b.Learn(m)
	return b.Forward(m)
}

func (b *VirtualBridge) Output(m *Framer) error {
	var err error
	if b.out.Has(libol.DEBUG) {
		b.out.Debug("VirtualBridge.Output: % x", m.Data[:20])
	}
	if dev := m.Output; dev != nil {
		_, err = dev.Send(m.Data)
	}
	return err
}

func (b *VirtualBridge) Eth2Str(addr []byte) string {
	if len(addr) < 6 {
		return ""
	}
	return fmt.Sprintf("%02x%02x%02x%02x%02x%02x",
		addr[0], addr[1], addr[2], addr[3], addr[4], addr[5])
}

func (b *VirtualBridge) Learn(m *Framer) {
	source := m.Data[6:12]
	if source[0]&0x01 == 0x01 {
		return
	}
	index := b.Eth2Str(source)
	if l := b.FindDest(index); l != nil {
		b.UpdateDest(index)
		return
	}
	learn := &Learner{
		Device:  m.Source,
		Uptime:  time.Now().Unix(),
		NewTime: time.Now().Unix(),
	}
	learn.Dest = make([]byte, 6)
	copy(learn.Dest, source)
	b.out.Info("VirtualBridge.Learn: %s on %s", index, m.Source)
	b.AddDest(index, learn)
}

func (b *VirtualBridge) FindDest(d string) *Learner {
	b.lock.RLock()
	defer b.lock.RUnlock()

	if l, ok := b.learners[d]; ok {
		return l
	}
	return nil
}

func (b *VirtualBridge) AddDest(d string, l *Learner) {
	b.lock.Lock()
	defer b.lock.Unlock()
	b.learners[d] = l
}

func (b *VirtualBridge) UpdateDest(d string) {
	b.lock.RLock()
	defer b.lock.RUnlock()
	if l, ok := b.learners[d]; ok {
		l.Uptime = time.Now().Unix()
	}
}

func (b *VirtualBridge) Flood(m *Framer) error {
	var err error
	data := m.Data
	src := m.Source
	if b.out.Has(libol.DEBUG) {
		b.out.Debug("VirtualBridge.Flood: % x", data[:20])
	}
	for _, dst := range b.devices {
		if src == dst {
			continue
		}
		_, err = dst.Send(data)
	}
	return err
}

func (b *VirtualBridge) Unicast(m *Framer) bool {
	data := m.Data
	src := m.Source
	index := b.Eth2Str(data[:6])

	if l := b.FindDest(index); l != nil {
		dst := l.Device
		if dst != src {
			if _, err := dst.Send(data); err != nil {
				b.out.Warn("VirtualBridge.Unicast: %s %s", dst, err)
			}
		}
		if b.out.Has(libol.DEBUG) {
			b.out.Debug("VirtualBridge.Unicast: %s to %s % x", src, dst, data[:20])
		}
		return true
	}
	return false
}

func (b *VirtualBridge) Mtu() int {
	return b.ifMtu
}

func (b *VirtualBridge) Stp(enable bool) error {
	return libol.NewErr("operation notSupport")
}

func (b *VirtualBridge) Delay(value int) error {
	return libol.NewErr("operation notSupport")
}
