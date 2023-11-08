This repository is the start of an RPC provider for DIN.

This plugin is intended to work in tandem with a separate middleware plugin
that takes care of routing to specific services. This plugin should have a
traefik config that looks something like:

```
providers:
  plugin:
    rpcrouter:
      providers:
        - url: https://provider1/
          wsurl: wss://provider1/ws
          methods:
            - eth_chainId
            - eth_blockNumber
        - url: https://provider2/
          wsurl: wss://provider2/ws
          methods:
            - eth_chainId
            - eth_getBlockByNumber
      methods:
        - Name: eth_chainId
        - Name: eth_blockNumber
        - Name: eth_getBlockByNumber
```

In this case, calls to `eth_chainid` might be routed to either provider, calls to 
`eth_blockNumber` will go to provider1, and calls to `eth_getBlockByNumber` will
go to provider2.

Block number awareness can be provided by the separate agent in the `/agent` directory. Running

```
go run agent/main.go https://traefikhost:7777
```

will connect the agent to the traefik plugin. The agent will retrieve information about
each provider's websocket endpoint from the plugin, subscribe via websockets, and make
calls to the traefik plugin to notify it of block updates. Traffic will only be served to
providers with the latest block number available.

In the future we may try to incorporate the agent back into the plugin, but at the moment
establishing websocket connections via Yaegi is proving difficult.