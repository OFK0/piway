package mdns

import (
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	avahiService = "org.freedesktop.Avahi"
	avahiServer  = avahiService + ".Server"
	avahiGroup   = avahiService + ".EntryGroup"

	ifaceUnspec = int32(-1) // every interface
	protoUnspec = int32(-1) // IPv4 and IPv6
	publishUniq = uint32(1) // AVAHI_PUBLISH_UNIQUE: probe, report collisions
	classIN     = uint16(1)
	typeCNAME   = uint16(5)
	ttl         = uint32(120) // avahi's default for host records
)

// DBus is the avahi daemon reached over the D-Bus system bus.
type DBus struct {
	conn *dbus.Conn
}

// Connect opens its own connection to the system bus. Entry groups
// belong to the connection: avahi withdraws them all when it closes,
// so names cannot outlive piway even if it is killed.
func Connect() (*DBus, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("mdns: system bus: %w", err)
	}
	d := &DBus{conn: conn}
	if _, err := d.HostName(); err != nil {
		conn.Close()
		return nil, err
	}
	return d, nil
}

func (d *DBus) Close() error { return d.conn.Close() }

func (d *DBus) HostName() (string, error) {
	var host string
	err := d.conn.Object(avahiService, "/").Call(avahiServer+".GetHostNameFqdn", 0).Store(&host)
	if err != nil {
		return "", fmt.Errorf("mdns: avahi: %w", err)
	}
	return host, nil
}

func (d *DBus) Alias(name, target string) (Group, error) {
	rdata, err := wireName(target)
	if err != nil {
		return nil, err
	}
	var path dbus.ObjectPath
	if err := d.conn.Object(avahiService, "/").Call(avahiServer+".EntryGroupNew", 0).Store(&path); err != nil {
		return nil, fmt.Errorf("mdns: avahi: %w", err)
	}
	g := &dbusGroup{obj: d.conn.Object(avahiService, path)}
	call := g.obj.Call(avahiGroup+".AddRecord", 0,
		ifaceUnspec, protoUnspec, publishUniq, name, classIN, typeCNAME, ttl, rdata)
	if call.Err == nil {
		call = g.obj.Call(avahiGroup+".Commit", 0)
	}
	if call.Err != nil {
		g.Free()
		return nil, fmt.Errorf("mdns: avahi: %w", call.Err)
	}
	return g, nil
}

type dbusGroup struct {
	obj dbus.BusObject
}

func (g *dbusGroup) State() (State, error) {
	var s int32
	if err := g.obj.Call(avahiGroup+".GetState", 0).Store(&s); err != nil {
		return 0, err
	}
	return State(s), nil
}

func (g *dbusGroup) Free() error { return g.obj.Call(avahiGroup+".Free", 0).Err }

// wireName encodes a domain name as DNS wire format: length-prefixed
// labels and a final zero, the RDATA of a CNAME record.
func wireName(name string) ([]byte, error) {
	var b []byte
	for l := range strings.SplitSeq(strings.TrimSuffix(name, "."), ".") {
		if l == "" || len(l) > 63 {
			return nil, fmt.Errorf("mdns: bad host name %q", name)
		}
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	if len(b) > 254 {
		return nil, errors.New("mdns: host name too long")
	}
	return append(b, 0), nil
}
