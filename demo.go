// Package pluginproviderdemo contains a demo of the provider's plugin.
package din_traefik_provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"net/http"
	"io/ioutil"

	"github.com/traefik/genconf/dynamic"

	"strings"
)

// Config the plugin configuration.
type CConfig struct {
	Providers []*ServiceProvider `json:"providers"`
	Methods []RPCMethod `json:"methods"`
}

type Config struct {
	Services map[string]CConfig `json:"services"`
}

type ServiceProvider struct {
	URL string `json:"url"`
	WSURL string `json:"wsurl"`
	Archive bool `json:"archive"`
	latestBlock int64
	service string
}

type newHeadsMessage struct {
	Params struct {
		Result struct {
			Number string `json:"number"`
		} `json:"result"`
	} `json:"params"`
}

type RPCMethod struct {
	Name string `json:"name"`
	BlockSensitive bool `json:"blockSensitive"`
	BlockSpecific bool `json:"blockSpecific"`
	BlockArg bool `json:"blockArg"`
}
// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{
		Services: make(map[string]CConfig),
	}
}

// Provider a simple provider plugin.
type Provider struct {
	name    string
	methods map[string][]RPCMethod
	serviceProvider map[string][]*ServiceProvider
	blockNumCh chan cBN

	cancel  func()
}


func Serve(ctx context.Context, providers []*ServiceProvider, port int64, blockNumCh chan cBN) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			data, _ := json.Marshal(providers)
			w.Write(data)
			return
		}
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			log.Printf("Error reading body: %v", err)
			http.Error(w, "Server Error1", http.StatusInternalServerError)
			return
		}
		var rpc map[string]int64
		if err := json.Unmarshal(body, &rpc); err != nil {
			log.Printf("Error unmarshalling")
			return
		}
		max := make(map[string]int64)
		for k, v := range rpc {
			idx, err := strconv.Atoi(k)
			if err != nil {
				http.Error(w, "Bad body", http.StatusInternalServerError)
				return
			}
			providers[idx].latestBlock = v
			if v > max[providers[idx].service] {
				max[providers[idx].service] = v
			}
		}
		for service, v := range max {
			blockNumCh <- cBN{service, v}
		}
		w.Write([]byte(`{"ok": true}`))
	})
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%v", port), nil))
	
}

type cBN struct {
	service string
	bn int64
}

// New creates a new Provider plugin.
func New(ctx context.Context, ccConfig *Config, name string) (*Provider, error) {
	sp := make(map[string][]*ServiceProvider)
	methods := make(map[string][]RPCMethod)
	providers := []*ServiceProvider{}
	cBNCh := make(chan cBN)
	for service, config := range ccConfig.Services {
		sp[service] = make([]*ServiceProvider, len(config.Providers))
		providers = append(providers, config.Providers...)
		methods[service] = config.Methods
		
		for i, provider := range config.Providers {
			// log.Printf("Provider: '%v' | '%v'", provider.URL, provider.Methods)
			provider.service = service
			sp[service][i] = provider
		}
	}
	go Serve(ctx, providers, 7777, cBNCh)
	// log.Printf("SP: %v", sp["eth"]["web3_clientVersion"])
	return &Provider{
		name:         name,
		methods: methods,
		serviceProvider: sp,
		blockNumCh: cBNCh,
	}, nil
}

// Init the provider.
func (p *Provider) Init() error {
	return nil
}

// Provide creates and send dynamic configuration.
func (p *Provider) Provide(cfgChan chan<- json.Marshaler) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	go func() {
		defer func() {
			if err := recover(); err != nil {
				log.Print(err)
			}
		}()

		p.loadConfiguration(ctx, cfgChan)
	}()
	p.blockNumCh <- cBN{}

	return nil
}

func (p *Provider) loadConfiguration(ctx context.Context, cfgChan chan<- json.Marshaler) {
	bn := make(map[string]int64)
	for {
		select {
		case block := <-p.blockNumCh:
			if block.bn >= bn[block.service] {
				bn[block.service] = block.bn
				configuration := p.generateConfiguration(bn)
				x, _ := json.Marshal(configuration)
				log.Printf("Config: %v", string(x))
				cfgChan <- &dynamic.JSONPayload{Configuration: configuration}
			}

		case <-ctx.Done():
			return
		}
	}
}

// Stop to stop the provider and the related go routines.
func (p *Provider) Stop() error {
	p.cancel()
	return nil
}

func(p *Provider) generateConfiguration(bn map[string]int64) *dynamic.Configuration {
	configuration := &dynamic.Configuration{
		HTTP: &dynamic.HTTPConfiguration{
			Routers:           make(map[string]*dynamic.Router),
			Middlewares:       make(map[string]*dynamic.Middleware),
			Services:          make(map[string]*dynamic.Service),
			ServersTransports: make(map[string]*dynamic.ServersTransport),
		},
	}

	configuration.HTTP.Middlewares["rpcloopback"] = &dynamic.Middleware{
		Plugin: make(map[string]dynamic.PluginConf),
	}
	configuration.HTTP.Middlewares["rpcloopback"].Plugin["rpcloopback"] = dynamic.PluginConf{}
	configuration.HTTP.ServersTransports["default"] = &dynamic.ServersTransport{
		DisableHTTP2: true,
	}

	// methods map[string][]RPCMethod
	// serviceProvider map[string]map[string][]*ServiceProvider
	for service, methods := range p.methods {
		loopback := fmt.Sprintf("%vloopback", service)
		configuration.HTTP.Routers[loopback] = &dynamic.Router{
			EntryPoints: []string{"web"},
			Service:     loopback,
			Rule:        fmt.Sprintf("Path(`/%v`)", service),
			Priority: 1,
			Middlewares: []string{"rpcloopback"},
		}
	
	
		configuration.HTTP.Services[loopback] = &dynamic.Service{
			LoadBalancer: &dynamic.ServersLoadBalancer{
				Servers: []dynamic.Server{
					{
						URL: "http://localhost:8000",
					},
				},
				PassHostHeader: boolPtr(true),
				ServersTransport: "default",
			},
		}
		servers := []dynamic.Server{}
		for _, sp := range p.serviceProvider[service] {
			if sp.latestBlock >= bn[service] {
				servers = append(servers, dynamic.Server{URL: sp.URL})
			}
		}
		if len(servers) == 0 {
			log.Printf("Warning: service %v has no health providers. Balancing across all.",  service)
			for _, sp := range p.serviceProvider[service] {
				servers = append(servers, dynamic.Server{URL: sp.URL})
			}
		}
		for _, method := range methods {
			configuration.HTTP.Services[fmt.Sprintf("%v%v", service, method.Name)] = &dynamic.Service{
				LoadBalancer: &dynamic.ServersLoadBalancer{
					PassHostHeader: boolPtr(false),
					Servers: servers,
					ServersTransport: "default",
				},
			}
			path := fmt.Sprintf("/%v/", service) + strings.Join(strings.Split(method.Name, "_"), "/")
			configuration.HTTP.Routers[fmt.Sprintf("%v%v", service, method.Name)] = &dynamic.Router{
				EntryPoints: []string{"web"},
				Service:     fmt.Sprintf("%v%v", service, method.Name),
				Rule:        fmt.Sprintf("PathPrefix(`%v`)", path),
				Priority: 2,
			}
		}

	}

	return configuration
}

func boolPtr(v bool) *bool {
	return &v
}
