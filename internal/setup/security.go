package setup

import (
	"context"
	"regexp"
	"strings"
)

// Security checks the steps in docs/HARDENING.md. It must run as root:
// sshd's effective settings and the firewall are root-only. user is the
// account piway runs as (SUDO_USER under sudo), whose SSH key and rootless
// Docker are checked; empty skips those checks. It changes nothing.
func Security(ctx context.Context, h Host, user string) *Report {
	c := &checker{ctx: ctx, h: h, env: Env{User: user}}
	if !h.Root() {
		c.section = "Security"
		c.add("root", "Permissions", Fail, "the security checks read root-only settings", "sudo ~/.local/bin/piway doctor --security")
		return &Report{Results: c.results}
	}
	c.ssh()
	c.firewall()
	c.fail2ban()
	c.updates()
	c.dockerSecurity()
	return &Report{Results: c.results}
}

func (c *checker) ssh() {
	c.section = "SSH"
	out, err := c.run("sshd", "-T")
	if err != nil || out == "" {
		c.add("sshd", "sshd settings", Fail, "cannot read sshd's settings (sshd -T)", "")
	} else {
		settings := map[string]string{}
		for line := range strings.Lines(out) {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
				settings[k] = v
			}
		}
		for _, s := range []struct {
			key, title, fix string
			miss            Status
		}{
			{"passwordauthentication", "Password login", "PasswordAuthentication no", Fail},
			{"kbdinteractiveauthentication", "Keyboard-interactive login", "KbdInteractiveAuthentication no", Fail},
			{"permitrootlogin", "Root login", "PermitRootLogin no", Fail},
			{"x11forwarding", "X11 forwarding", "X11Forwarding no", Warn},
		} {
			if v := settings[s.key]; v == "no" {
				c.add("ssh-"+s.key, s.title, OK, "off", "")
			} else {
				c.add("ssh-"+s.key, s.title, s.miss, "is "+v,
					"add `"+s.fix+"` to /etc/ssh/sshd_config.d/10-hardening.conf (it must sort before 50-cloud-init.conf)\nsudo sshd -t && sudo systemctl reload ssh")
			}
		}
	}

	if c.env.User == "" {
		return
	}
	home := c.homeOf(c.env.User)
	if b, err := c.h.ReadFile(home + "/.ssh/authorized_keys"); err == nil && strings.TrimSpace(string(b)) != "" {
		c.add("ssh-key", "SSH key", OK, c.env.User+" has an authorized key", "")
	} else {
		c.add("ssh-key", "SSH key", Fail, c.env.User+" has no ~/.ssh/authorized_keys; turning off password login would lock you out",
			"ssh-copy-id "+c.env.User+"@<pi>   # from your computer")
	}
}

// homeOf reads the user's home directory from the passwd database.
func (c *checker) homeOf(user string) string {
	out, err := c.run("getent", "passwd", user)
	if f := strings.Split(out, ":"); err == nil && len(f) >= 6 {
		return f[5]
	}
	return "/home/" + user
}

var sshLimit = regexp.MustCompile(`(?m)^22/tcp .*LIMIT`)

func (c *checker) firewall() {
	c.section = "Firewall"
	if _, err := c.h.LookPath("ufw"); err != nil {
		c.add("ufw", "ufw", Fail, "not installed", "sudo apt install ufw   # then the rules in docs/HARDENING.md")
		return
	}
	out, _ := c.run("ufw", "status", "verbose")
	if strings.Contains(out, "Status: active") {
		c.add("ufw", "ufw", OK, "active", "")
	} else {
		c.add("ufw", "ufw", Fail, "installed but inactive", "sudo ufw enable")
	}
	if strings.Contains(out, "deny (incoming)") {
		c.add("ufw-default", "Incoming", OK, "denied by default", "")
	} else {
		c.add("ufw-default", "Incoming", Fail, "not denied by default", "sudo ufw default deny incoming")
	}
	if sshLimit.MatchString(out) {
		c.add("ufw-ssh", "SSH rate limit", OK, "on", "")
	} else {
		c.add("ufw-ssh", "SSH rate limit", Warn, "no `limit` rule for SSH", "sudo ufw limit from <lan>/24 to any port 22 proto tcp   # docs/HARDENING.md")
	}
	if active, _ := c.run("systemctl", "is-active", "ufw-safety.timer"); active == "active" {
		c.add("ufw-safety", "Safety timer", Warn, "still pending: ufw turns itself off when it fires", "sudo systemctl stop ufw-safety.timer")
	}
}

func (c *checker) fail2ban() {
	c.section = "fail2ban"
	if _, err := c.h.LookPath("fail2ban-client"); err != nil {
		c.add("fail2ban", "fail2ban", Fail, "not installed", "sudo apt install fail2ban   # then the jail in docs/HARDENING.md")
		return
	}
	if out, _ := c.run("systemctl", "is-active", "fail2ban"); out == "active" {
		c.add("fail2ban", "fail2ban", OK, "running", "")
	} else {
		c.add("fail2ban", "fail2ban", Fail, "not running", "sudo systemctl enable --now fail2ban")
	}
	if _, err := c.run("fail2ban-client", "status", "sshd"); err == nil {
		c.add("fail2ban-sshd", "sshd jail", OK, "on", "")
	} else {
		c.add("fail2ban-sshd", "sshd jail", Fail, "off", "/etc/fail2ban/jail.d/piway.local from docs/HARDENING.md, then: sudo systemctl restart fail2ban")
	}
}

func (c *checker) updates() {
	c.section = "Automatic updates"
	if _, err := c.h.Stat("/usr/bin/unattended-upgrade"); err != nil {
		c.add("unattended", "Unattended upgrades", Fail, "not installed", "sudo apt install unattended-upgrades")
		return
	}
	dump, _ := c.run("apt-config", "dump")
	if strings.Contains(dump, `APT::Periodic::Unattended-Upgrade "1"`) {
		c.add("unattended", "Unattended upgrades", OK, "enabled", "")
	} else {
		c.add("unattended", "Unattended upgrades", Fail, "installed but not enabled", "sudo dpkg-reconfigure -plow unattended-upgrades")
	}
	if strings.Contains(dump, `Origins-Pattern:: "origin=Raspberry Pi Foundation`) {
		c.add("unattended-rpi", "Raspberry Pi archive", OK, "included", "")
	} else {
		c.add("unattended-rpi", "Raspberry Pi archive", Fail, "firmware and kernel updates are not applied automatically", "/etc/apt/apt.conf.d/52piway-unattended-upgrades from docs/HARDENING.md")
	}
	if out, _ := c.run("systemctl", "is-active", "apt-daily-upgrade.timer"); out == "active" {
		c.add("upgrade-timer", "Upgrade timer", OK, "active", "")
	} else {
		c.add("upgrade-timer", "Upgrade timer", Fail, "inactive", "sudo systemctl enable --now apt-daily-upgrade.timer")
	}
}

func (c *checker) dockerSecurity() {
	c.section = "Docker"
	switch out, _ := c.run("systemctl", "is-active", "docker.service"); out {
	case "active":
		c.add("root-daemon", "Root daemon", Fail, "running; anyone in the docker group is root", "sudo systemctl disable --now docker.service docker.socket")
	case "inactive", "failed":
		c.add("root-daemon", "Root daemon", OK, "off", "")
	default:
		c.add("root-daemon", "Root daemon", Warn, "cannot tell whether it runs", "")
	}
	if c.env.User == "" {
		return
	}
	uid, err := c.run("id", "-u", c.env.User)
	if err != nil {
		return
	}
	out, _ := c.run("runuser", "-u", c.env.User, "--", "env", "XDG_RUNTIME_DIR=/run/user/"+uid, "systemctl", "--user", "is-active", "docker")
	if out == "active" {
		c.add("rootless", "Rootless daemon", OK, "running for "+c.env.User, "")
	} else {
		c.add("rootless", "Rootless daemon", Warn, "not running for "+c.env.User, "systemctl --user enable --now docker   # as "+c.env.User)
	}
}
