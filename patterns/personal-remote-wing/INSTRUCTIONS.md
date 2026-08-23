# Reach a personal machine from the web

Install Wingthing on the machine that will run the agents:

```sh
curl -fsSL https://wingthing.ai/install.sh | sh
wt login
wt start
```

Open `https://app.wingthing.ai`, select the wing, and start or resume an egg.
The wing connects outbound to the roost, so the machine needs no inbound port.

Check the daemon and active eggs from the machine:

```sh
wt wing status
wt attach
```
