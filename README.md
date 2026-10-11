# distribrr

        ·▄▄▄▄  ▪  .▄▄ · ▄▄▄▄▄▄▄▄  ▪  ▄▄▄▄· ▄▄▄  ▄▄▄  
        ██▪ ██ ██ ▐█ ▀. •██  ▀▄ █·██ ▐█ ▀█▪▀▄ █·▀▄ █·
        ▐█· ▐█▌▐█·▄▀▀▀█▄ ▐█.▪▐▀▀▄ ▐█·▐█▀▀█▄▐▀▀▄ ▐▀▀▄
        ██. ██ ▐█▌▐█▄▪▐█ ▐█▌·▐█•█▌▐█▌██▄▪▐█▐█•█▌▐█•█▌
        ▀▀▀▀▀• ▀▀▀ ▀▀▀▀  ▀▀▀ .▀  ▀▀▀▀·▀▀▀▀ .▀  ▀.▀  ▀

distribrr is a companion to autobrr to distribute downloads across multiple servers.

- Single binary that can run as either agent or server
- Supported clients: qBittorrent
- Read filesystem

## Server

You need to run one server that manages agents.

### Run

    distribrr server run

### Tokens

The server and every agent need an API token (`http.token`), at least 16 characters. They refuse to start without
one. Generate one with:

    distribrr token generate

The server's token goes in autobrr's webhook URL and in every agent's `manager.token`. Each agent's own `http.token`
can be different; the agent sends it when it registers.

## Agent

The agent runs on remote servers alongside the torrent clients and has access to the filesystem.

### Run

    distribrr agent run

## Logging

Both roles log to stdout. Set `LOG_LEVEL` to `trace`, `debug`, `info` (default), `warn` or `error`.

Each request gets a correlation id, taken from the `X-Correlation-ID` header or generated. The server forwards it to the
agents for a task, so one webhook's log lines share the same `correlation_id` on the server and every agent.

## Network security

distribrr speaks plain HTTP. Anyone who can watch the traffic between autobrr, the server and the agents can read:

- the server's token, in the webhook URL and in every agent's register request
- each agent's token, sent by the server on every call to that agent
- the `download_url` of every task, which usually carries your tracker passkey

The server also has to reach every agent's `clientAddr`: it calls the agents, not the other way round. An agent behind
NAT or a provider firewall needs a way in.

Plain HTTP is fine when everything runs on one host or on a network you trust. Then bind to that network instead of all
interfaces, e.g. `http.host: 127.0.0.1` when autobrr, the server and an agent share a host.

Otherwise, use one of these.

### Private network

Run the server and the agents on a WireGuard or Tailscale network and use their tunnel addresses:

- `http.host`: the tunnel IP, so distribrr does not listen on the public interface
- the agent's `agent.clientAddr` and `manager.addr`, and the autobrr webhook endpoint: tunnel addresses

This also solves NAT: every node can reach every other over the tunnel.

### TLS reverse proxy

Put a reverse proxy with a certificate in front of the server and of each agent, bind distribrr to `127.0.0.1`, and
use `https://` everywhere. With [Caddy](https://caddyserver.com), on the server host:

    distribrr.example.com {
        reverse_proxy 127.0.0.1:7422
    }

and on each agent host:

    agent1.example.com {
        reverse_proxy 127.0.0.1:7430
    }

Then set:

- the agent's `agent.clientAddr`: `https://agent1.example.com`
- the agent's `manager.addr`: `https://distribrr.example.com`
- the autobrr webhook endpoint: `https://distribrr.example.com/api/v1/tasks?apikey=...`

The certificates must be trusted by the system: distribrr has no option to skip verification, so a self-signed
certificate fails unless you add its CA to the host's trust store, or point `SSL_CERT_FILE` at it when starting
distribrr.

Keep the proxy's response timeout above 120s. A webhook can wait up to 100s for the agents, and an agent up to 60s for
the `.torrent` and qBittorrent. Caddy has no response timeout by default; nginx cuts responses after 60s unless you
raise `proxy_read_timeout`.

### Token in the URL

The server accepts its token in the `X-API-Token` or `Authorization` header as well as in `?apikey=`. Query strings end
up in proxy access logs, so prefer a header wherever the caller can send one.

## Autobrr usage

To use with autobrr set up a new action of type `Webhook` and use the following:

1. Endpoint: `http://localhost:7422/api/v1/tasks?apikey=YOUR_SECRET_TOKEN`

2. Payload:
    ```json
    {
      "download_url": "{{ .DownloadUrl }}",
      "name": "{{ .TorrentName }}",
      "max_replicas": 2,
      "scheduler_type": "least_active",
      "category": "race",
      "tags": "race,distribrr",
      "indexer": "{{ .IndexerName }}",
      "size": {{ .Size }},
      "labels": {
        "region": "test"
      }
    }
    ```

   `scheduler_type` is optional and picks how nodes are chosen for this filter:
   - `least_active` (default): the nodes with the fewest and furthest along active downloads.
   - `round_robin`: nodes take turns in name order, whatever their load.

   Both only consider ready nodes that match `labels`. An unknown value is rejected with `400`.

   `max_replicas` is the number of nodes that get the torrent (default `1`). Each node adds it to one of its clients:
   the least active ready client with room, then the one with the most storage headroom. If that add fails, the node
   tries its next client.

    `size` is optional. When it is set, the server skips nodes where no client has storage room for the torrent, so another node can take it. It is `0` when autobrr does not know the size yet; the agent then checks against the size from the `.torrent` file.

## Flow

    announce -> autobrr -> filters -> actions -> distribrr
                                                    \  \
                                                     \  + agent -> torrent client(s)
                                                      + agent -> torrent client(s)