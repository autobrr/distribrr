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

## Agent

The agent runs on remote servers alongside the torrent clients and has access to the filesystem.

### Run

    distribrr agent run

## Autobrr usage

To use with autobrr set up a new action of type `Webhook` and use the following:

1. Endpoint: `http://localhost:7422/api/v1/tasks?apikey=YOUR_SECRET_TOKEN`

2. Payload:
    ```json
    {
      "download_url": "{{ .DownloadURL }}",
      "name": "{{ .TorrentName }}",
      "max_downloads": 2,
      "category": "race-test",
      "tags": "tag1,tag2"
    }
    ```

## Flow

    announce -> autobrr -> filters -> actions -> distribrr
                                                    \  \
                                                     \  + agent -> torrent client(s)
                                                      + agent -> torrent client(s)