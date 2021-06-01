package registry

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

/* *****************************************************
一个简单的支持心跳的注册中心：
1. 服务注册
2. 接收服务器实例的心跳包
3. 查询可用服务并返回
***************************************************** */
type GeeRegistry struct {
	timeout time.Duration // 服务超时时间 默认为5分钟
	mu      sync.Mutex
	servers map[string]*ServerItem
}

type ServerItem struct { // 服务实例
	Addr  string
	start time.Time // 用于检查服务是否超时
}

const (
	defaultPath    = "/_geerpc_/registry"
	defaultTimeout = time.Minute * 5
)

func New(timeout time.Duration) *GeeRegistry {
	return &GeeRegistry{
		servers: make(map[string]*ServerItem),
		timeout: timeout,
	}
}

var DefaultGeeRegistry = New(defaultTimeout)

func (r *GeeRegistry) putServer(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[addr]
	if s == nil {
		r.servers[addr] = &ServerItem{Addr: addr, start: time.Now()}
	} else {
		s.start = time.Now()
	}
}

func (r *GeeRegistry) aliveServers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var alive []string
	for addr, s := range r.servers {
		// r.timeout = 0 表示不限制服务超时时间
		if r.timeout == 0 || s.start.Add(r.timeout).After(time.Now()) {
			alive = append(alive, addr)
		} else {
			delete(r.servers, addr)
		}
	}
	sort.Strings(alive) // 这里排序是为了让客户端实现有效的负载均衡 比如轮询
	return alive
}

func (r *GeeRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET": // GET返回所有可用的服务实例列表 承载在HTTP Header中 —— X-Geerpc-Servers
		w.Header().Set("X-Geerpc-Servers", strings.Join(r.aliveServers(), ","))
	case "POST": // 添加服务实例或发送心跳 承载在HTTP Header中 —— X-Geerpc-Server
		addr := req.Header.Get("X-Geerpc-Server")
		if addr == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.putServer(addr)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (r *GeeRegistry) HandleHTTP(regisrtyPath string) {
	http.Handle(regisrtyPath, r)
	log.Println("rpc registry path: ", regisrtyPath)
}

func HandleHTTP() {
	DefaultGeeRegistry.HandleHTTP(defaultPath)
}

// 便于服务启动定时向注册中心发送心跳 默认周期比注册中心设置的过期时间少1min
func Heartbeat(registry, addr string, duration time.Duration) {
	if duration == 0 {
		duration = defaultTimeout - time.Duration(1)*time.Minute
	}
	var err error
	err = sendHeartbeat(registry, addr)
	go func() {
		t := time.NewTicker(duration) // 定时计时器
		for err == nil {
			<-t.C
			err = sendHeartbeat(registry, addr)
		}
	}()
}

func sendHeartbeat(registry, addr string) error {
	log.Println(addr, "send heart beat to registry", registry)
	httpClient := &http.Client{}
	req, _ := http.NewRequest("POST", registry, nil)
	req.Header.Set("X-Geerpc-Server", addr)
	// http.Client.Do：发现HTTP请求并返回一个HTTP响应
	if _, err := httpClient.Do(req); err != nil {
		log.Println("rpc server: heat beat err:", err)
		return err
	}
	return nil
}
