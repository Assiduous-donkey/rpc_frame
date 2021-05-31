package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

type SelectMode int // 表示不同的负载均衡策略

const (
	RandomSelect     SelectMode = iota // 随机选择
	RoundRobinSelect                   // 轮询
)

type Discovery interface {
	Refresh() error                      // 从注册中心更新服务列表
	Update(servers []string) error       // 手动更新服务列表
	Get(mode SelectMode) (string, error) // 根据负载均衡策略选择一个服务实例
	GetAll() ([]string, error)           // 获取所有服务实例
}

// 手工维护服务列表，不需要注册中心的服务发现结构体
type MultiServerDiscovery struct {
	r       *rand.Rand // 获取随机数
	mu      sync.RWMutex
	servers []string
	index   int // 用于轮询策略 记录已经轮询到的位置
}

func NewMultiServerDiscovery(servers []string) *MultiServerDiscovery {
	d := &MultiServerDiscovery{
		servers: servers,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	d.index = d.r.Intn(math.MaxInt32 - 1) // 初始化一个随机值 避免每次从0开始
	return d
}

var _ Discovery = (*MultiServerDiscovery)(nil)

func (d *MultiServerDiscovery) Refresh() error {
	return nil
}

func (d *MultiServerDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers // slice非线程安全
	return nil
}

func (d *MultiServerDiscovery) Get(mode SelectMode) (string, error) {
	d.mu.RLock() // 并发情况下slice非线程安全
	defer d.mu.RUnlock()

	n := len(d.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}
	switch mode {
	case RandomSelect:
		return d.servers[d.r.Intn(n)], nil
	case RoundRobinSelect:
		s := d.servers[d.index%n]
		d.index = (d.index + 1) % n
		return s, nil
	default:
		return "", errors.New("rpc discovery: not supported select mode")
	}
}

func (d *MultiServerDiscovery) GetAll() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	servers := make([]string, len(d.servers), len(d.servers))
	copy(servers, d.servers)
	return servers, nil
}
