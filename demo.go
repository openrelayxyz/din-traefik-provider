// Package pluginproviderdemo contains a demo of the provider's plugin.
package pluginproviderdemo

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
	Networks map[string]CConfig `json:"networks"`
}

type ServiceProvider struct {
	URL string `json:"url"`
	WSURL string `json:"wsurl"`
	Methods []RPCMethod `json:"methods"`
	Archive bool `json:"archive"`
	latestBlock int64
	chain string
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
		Networks: make(map[string]CConfig),
	}
}

// Provider a simple provider plugin.
type Provider struct {
	name    string
	methods map[string][]RPCMethod
	serviceProvider map[string]map[string][]*ServiceProvider
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
			if v > max[providers[idx].chain] {
				max[providers[idx].chain] = v
			}
		}
		for chain, v := range max {
			blockNumCh <- cBN{chain, v}
		}
		w.Write([]byte(`{"ok": true}`))
	})
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%v", port), nil))
	
}

type cBN struct {
	chain string
	bn int64
}

// New creates a new Provider plugin.
func New(ctx context.Context, ccConfig *Config, name string) (*Provider, error) {
	sp := make(map[string]map[string][]*ServiceProvider)
	methods := make(map[string][]RPCMethod)
	providers := []*ServiceProvider{}
	cBNCh := make(chan cBN)
	for chain, config := range ccConfig.Networks {
		sp[chain] = make(map[string][]*ServiceProvider)
		providers = append(providers, config.Providers...)
		methods[chain] = config.Methods
		
		for _, provider := range config.Providers {
			log.Printf("Provider: '%v' | '%v'", provider.URL, provider.Methods)
			provider.chain = chain
			for _, m := range provider.Methods {
				if _, ok := sp[chain][m.Name]; !ok {
					sp[chain][m.Name] = []*ServiceProvider{}
				}
				sp[chain][m.Name] = append(sp[chain][m.Name], provider)
			}
		}
	}
	go Serve(ctx, providers, 7777, cBNCh)
	log.Printf("SP: %v", sp["eth"]["web3_clientVersion"])
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
			if block.bn >= bn[block.chain] {
				bn[block.chain] = block.bn
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

	// methods map[string][]RPCMethod
	// serviceProvider map[string]map[string][]*ServiceProvider
	for chain, methods := range p.methods {
		loopback := fmt.Sprintf("%vloopback", chain)
		configuration.HTTP.Routers[loopback] = &dynamic.Router{
			EntryPoints: []string{"web"},
			Service:     loopback,
			Rule:        fmt.Sprintf("Path(`/%v`)", chain),
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
			},
		}
	
		for _, method := range methods {
			servers := []dynamic.Server{}
			for _, sp := range p.serviceProvider[chain][method.Name] {
				log.Printf("Method: %v, SP: %v, Chain: %v", method.Name, sp.URL, chain)
				if sp.latestBlock >= bn[chain] {
					servers = append(servers, dynamic.Server{URL: sp.URL})
				}
			}
			if len(servers) == 0 {
				log.Printf("Warning: method %v has no health providers. Balancing across all.",  method.Name)
				for _, sp := range p.serviceProvider[chain][method.Name] {
					servers = append(servers, dynamic.Server{URL: sp.URL})
				}
			}
			configuration.HTTP.Services[fmt.Sprintf("%v%v", chain, method.Name)] = &dynamic.Service{
				LoadBalancer: &dynamic.ServersLoadBalancer{
					PassHostHeader: boolPtr(false),
					Servers: servers,
				},
			}
			path := fmt.Sprintf("/%v/", chain) + strings.Join(strings.Split(method.Name, "_"), "/")
			configuration.HTTP.Routers[fmt.Sprintf("%v%v", chain, method.Name)] = &dynamic.Router{
				EntryPoints: []string{"web"},
				Service:     fmt.Sprintf("%v%v", chain, method.Name),
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
