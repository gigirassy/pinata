# Pinata is Yet Another Pinterest Frontend.

<img src="https://codeberg.org/gigirassy/pinata/raw/branch/main/screenies/pinata.png" alt="pinata-screenshot">

Self-explanatory. It's built with the idea of being light as possible, and not strain the end user's system while being fun.

The Docker image, built by you, ranges between 5-7 MB, and the entire workings depend on a single main.go.

* Pinata by itself takes very little memory to run, about 14MB of memory with docker image when active (30~ MB with Chunk Mode enabled), and 6MB when idle.
* The frontend uses no Javascript, which makes it friendly to low-end systems like netbooks!
* Pinata does not need a database to function, and all customization and saved bookmarks is done with encrypted cookies. No logs are kept in the container.
* Memory isn't a concern? Bring your own image proxy backend if you'd like! This allows you to compress images for the end user (making it even lighter), rotate ips, and do other fun stuff. An example that auto-compresses images: https://codeberg.org/gigirassy/image-proxy/

Despite being so light, Pinata supports bookmarks via cookies (encrypted!) and can be made a PWA on your phone.

**Contribute to <a href="https://codeberg.org/gigirassy/pinata/">this repo on Codeberg</a>!**

## How to run

### Go

**not recommended.**

Port 8080 is needed to run with this method; Docker is most recommended if that is taken.

* Clone this repo.
* (optional, but bookmarks will be unavailable) ``head -c 32 /dev/urandom | base64`` and then ``export PINATA_BOOKMARK_KEY=resultofpreviouscommand``.
* ``go build -trimpath -ldflags="-s -w" -o pinata ./main.go``
* Wait a few seconds for that tasty binary.
* Run in background with ``./pinata &``

### Compose (recommended)

* Clone this repo.
* Tweak ``compose.yml`` as you see fit and follow instructions if you want to enable bookmarks.
* Comment out image proxy backend and PINATA_IMAGE_BACKEND environment variable if memory is a concern.
* ``docker compose up -d``
* ``docker compose pull && docker compose up -d`` to update.
