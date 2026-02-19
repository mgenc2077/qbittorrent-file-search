## Introduction
This is a simple web application that allows you to search for files in your qBittorrent client. Uses postgresql to cache file names and search (sql LIKE). DB operations run concurrently so it can create load on the docker host.

![qbittorrent-file-search](https://github.com/mgenc2077/qbittorrent-file-search/blob/main/screenshot.png?raw=true)

## Features
- Search for files in your qBittorrent client
- Cache file names in postgresql
- Search (sql LIKE)
- Enable download for files

## Todo
- Create a dockerfile
- Add category filter for torrents

## Prequisites
Docker Compose with Postgre DB
```bash
docker compose up -d
```

## How to Build
```bash
go build -o qbittorrent-file-search.exe
```

## How to Run
```bash
./qbittorrent-file-search.exe
```

## How to Use
1. Open http://localhost:1182 in your browser
2. Enter a query in the search bar
3. Click the search button
4. Click on the hash to copy it to the clipboard