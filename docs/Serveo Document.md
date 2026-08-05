## Documentation

Complete guide to all Serveo features and configuration options

### Basic use

```
ssh -R 80:localhost:3000 serveo.net
```

The -R option instructs your SSH client to request port forwarding from the server and proxy requests to the specified host and port (usually localhost). A subdomain of serveo.net will be assigned to forward HTTP traffic.

### Request multiple tunnels at once

```
ssh -R 80:localhost:8888 -R 80:localhost:9999 serveo.net
```

### The target server doesn't have to be on localhost

```
ssh -R 80:example.com:80 serveo.net
```

### Request a particular subdomain

The subdomain is chosen deterministically based on your IP address, the provided SSH username, and subdomain availability, so you'll often get the same subdomain between restarts. You can also request a particular subdomain:

```
ssh -R incubo:80:localhost:8888 serveo.net
```

```
ssh -R incubo.serveo.net:80:localhost:8888 serveo.net
```

Change the SSH username to get assigned a different subdomain:

```
ssh -R 80:localhost:8888 foo@serveo.net
```

```
ssh -R 80:localhost:8888 -l foo serveo.net
```

### Private TCP and SSH forwarding

Serveo can be used to route private TCP traffic, almost like a lightweight VPN. To set up the tunnel, specify an alias as the hostname and some port:

```
ssh -R myalias:5901:localhost:5900 serveo.net
```

Then to connect to that port from another machine, use ssh -L:

```
ssh -L 5902:myalias:5901 serveo.net
```

Then connect to localhost:5902 on the remote machine, and SSH will send traffic through Serveo, which will forward it to the target machine, ultimately connecting you to port 5900 on the target machine.

If you're using this to connect to an SSH server, then you can use OpenSSH's JumpHost feature. On the target machine, you might start the tunnel like this:

```
ssh -R myalias:22:localhost:22 serveo.net
```

Then you can establish an SSH connection using serveo.net as an intermediary like this:

```
ssh -J serveo.net user@myalias
```

The -J option was introduced in the OpenSSH client version 7.3. If you have an older client, you can use the ProxyCommand option instead:

```
ssh -o ProxyCommand="ssh -W myalias:22 serveo.net" user@myalias
```

### Public TCP forwarding

If you request a port other than 80 or 443, raw TCP traffic will be forwarded. (In this case, there's no way to route connections based on hostname, and the host, if specified, will trigger private TCP forwarding.)

This feature is only available to registered users.

```
ssh -R 1492:localhost:1492 serveo.net
```

If port 0 is requested, a random TCP port will be forwarded:

```
ssh -R 0:localhost:1492 serveo.net
```

### Connect on port 443

In some environments, outbound port 22 connections are blocked. For this reason, you can also connect on port 443.

```
ssh -p 443 -R 80:localhost:8888 serveo.net
```

### Automatically reconnect

Use autossh for more persistent tunnels. Use "-M 0" to disable autossh's connectivity checking:

```
autossh -M 0 -R 80:localhost:8888 serveo.net
```

See [https://www.everythingcli.org/ssh-tunnelling-for-fun-and-profit-autossh/](https://www.everythingcli.org/ssh-tunnelling-for-fun-and-profit-autossh/) for more about autossh.

### Browser Warning

To prevent phishing and abuse, Serveo displays an interstitial warning page when browsers visit endpoints served by anonymous tunnels (SSH or WireGuard). Users can bypass this warning by clicking "Continue".

For automated access (like APIs), you can bypass the warning by adding the following request header:

```
serveo-skip-browser-warning: true
```

### WireGuard Tunnels

As an alternative to SSH, Serveo supports WireGuard, a modern, high-performance VPN protocol. WireGuard connections are just as private as SSH but are often more efficient and can provide a better experience for permanent tunnels.

To get started:

1. Log in to the Serveo Console and navigate to the **WireGuard Keys** section.
2. Click "Add Key" to generate a new key pair (or add your own public key).
3. Copy the generated WireGuard configuration block to your WireGuard client.
4. In the console, go to **WireGuard > HTTP Forwarding** and add a new rule linking a domain and port to your WireGuard key.
5. Activate the tunnel in your WireGuard client. This configuration is compatible with common tools like `wg-quick` as well as the official WireGuard clients for Windows and macOS.

Once connected, Serveo will forward traffic for that domain to the specified port on your device over the secure WireGuard tunnel.

### Custom Domain

To use your own domain or subdomain, you'll first need an SSH key pair. Use the ssh-keygen program to generate a key pair if you don't already have one.

Next, use ssh-keygen -l and note your key's fingerprint. Here's an example output:

```
2048 SHA256:pmc7ZRv7ymCmghUwHoJWEm5ToSTd33ryeDeps5RnfRY no comment (RSA)
```

In this example, the fingerprint is SHA256:pmc7ZRv7ymCmghUwHoJWEm5ToSTd33ryeDeps5RnfRY.

Now you need to add two DNS records for the domain or subdomain you'd like to use:

- A CNAME record pointing to serveo.net.
- For each SSH key to allow, a TXT record at _serveo-authkey.[domain] = [fingerprint].

Once your DNS records are in place, you can request your subdomain/domain from Serveo:

```
ssh -R subdomain.example.com:80:localhost:3000 serveo.net
```

When you request port forwarding for subdomain.example.com, Serveo will fetch the TXT records from your DNS server and only allow forwarding if you've provided a public key with the same fingerprint as specified in TXT records.