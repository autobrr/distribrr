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

## Flow

    announce -> autobrr -> filters -> actions -> distribrr
                                                    \  \
                                                     \  + agent -> torrent client(s)
                                                      + agent -> torrent client(s)