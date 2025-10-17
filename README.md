# Pinata is Yet Another Pinterest Frontend.

<img src="https://codeberg.org/gigirassy/pinata/raw/branch/main/screenies/pinata.png" alt="pinata-screenshot">

Self-explanatory. It's built with the idea of being light as possible, and shamefully vibecoded.

The Docker image, built by you, ranges between 5-7 MB, and the entire workings depend on a single main.go.

Pinata takes very little memory to run, about 7MB of memory with an Alpine docker image when active, and 1MB when idle.

Despite being so light, Pinata supports bookmarks (encrypted!) and can be made a PWA on your phone.


**Contribute to <a href="https://codeberg.org/gigirassy/pinata/">this repo on Codeberg</a>! While the image is hosted on a Github mirror, the code itself is edited on Codeberg!!**

## How to run

### Go

Port 8080 is needed to run with this method; Docker is most recommended if that is taken.

* Clone this repo.
* (optional, but bookmarks will be unavailable) ``head -c 32 /dev/urandom | base64`` and then ``export PINATA_BOOKMARK_KEY=resultofpreviouscommand``.
* ``go build -trimpath -ldflags="-s -w" -o pinata ./main.go``
* Wait a few seconds for that tasty binary.
* Run in background with ``./pinata &``

### Docker Compose (recommended)

* Clone this repo.
* Tweak ``compose.yml`` as you see fit and follow instructions if you want to enable bookmarks.
* ``sudo docker compose up -d`` to build and run.
* Need to update? ``git pull && docker compose up -d --build``
