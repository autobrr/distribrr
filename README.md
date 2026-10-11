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

    `size` is optional. When it is set, the server skips nodes where no client has storage room for the torrent, so another node can take it. It is `0` when autobrr does not know the size yet; the agent then checks against the size from the `.torrent` file.

## Flow

    announce -> autobrr -> filters -> actions -> distribrr
                                                    \  \
                                                     \  + agent -> torrent client(s)
                                                      + agent -> torrent client(s)