---
layout: home
hero:
  name: piway
  text: Your Raspberry Pi, run like a small cloud.
  tagline: A live metrics dashboard, git push deploys to your own subdomains, and an MCP server so Claude Code can operate it. One Go binary.
  actions:
    - theme: brand
      text: Install
      link: /INSTALL
    - theme: alt
      text: Deploy an app
      link: /DEPLOY
features:
  - title: One command to install
    details: install.sh verifies the release and runs piway setup, which installs the service, rootless Docker and Caddy, showing every sudo command first.
    link: /INSTALL
  - title: Live dashboard
    details: CPU, memory, temperature, throttling, disks and network every second, with history kept gently on the SD card.
    link: /STORAGE
  - title: git push to deploy
    details: A GitHub webhook builds an arm64 image, health-checks it and swaps it in at &lt;name&gt;.local with no downtime.
    link: /DEPLOY
  - title: Alerts on your phone
    details: Rules for heat, throttling, disk and failed deploys, sent as Web Push notifications.
    link: /ALERTS
  - title: Claude Code built in
    details: An MCP server with tools to check status, deploy, restart, roll back and read logs.
    link: /MCP
  - title: Safe by default
    details: Password and API tokens, Tailscale for remote access, and piway doctor --security for the hardening checklist.
    link: /HARDENING
---
