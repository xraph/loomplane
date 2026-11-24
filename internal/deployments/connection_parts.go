package deployments

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// schemePorts are the ports an address's scheme means when it names none.
var schemePorts = map[string]string{
	"postgres": "5432", "postgresql": "5432", "mysql": "3306",
	"redis": "6379", "rediss": "6379", "mongodb": "27017", "mongodb+srv": "27017",
}

// connectionPart is one piece of a resource's address, for a service that
// reads it in separate variables: its host, port, user, password or
// database. A replica set's address gives its first host. The address
// holds credentials, so no error repeats any of it.
func connectionPart(address, part string) (string, error) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(address), "://")
	if !ok || scheme == "" {
		return "", errors.New("the resource's address isn't a URL, so it can't be read in parts")
	}
	scheme = strings.ToLower(scheme)
	// The user info ends at the last @ before the path, the way drivers
	// read it; the host list ends at the first / or ? after that.
	at := strings.LastIndex(rest, "@")
	tail := rest[at+1:]
	end := len(tail)
	if i := strings.IndexAny(tail, "/?"); i >= 0 {
		end = i
	}
	info, hosts, after := "", tail[:end], tail[end:]
	if at >= 0 {
		info = rest[:at]
	}
	path, _, _ := strings.Cut(after, "?")
	first, _, _ := strings.Cut(hosts, ",")
	decode := func(s string) (string, error) {
		v, err := url.PathUnescape(s)
		if err != nil {
			return "", errors.New("the resource's address has an escape that can't be read")
		}
		return v, nil
	}
	switch part {
	case "host", "port":
		if first == "" {
			return "", errors.New("the resource's address names no host")
		}
		host, port, err := net.SplitHostPort(first)
		if err != nil {
			host, port = strings.Trim(first, "[]"), schemePorts[scheme]
		}
		if part == "host" {
			return host, nil
		}
		if _, perr := strconv.Atoi(port); perr != nil {
			return "", errors.New("the resource's address names no port, and its scheme has none by default")
		}
		return port, nil
	case "user":
		user, _, _ := strings.Cut(info, ":")
		return decode(user)
	case "password":
		_, password, _ := strings.Cut(info, ":")
		return decode(password)
	case "database":
		return decode(strings.TrimPrefix(path, "/"))
	}
	return "", errors.New("a variable can hold the address's host, port, user, password or database")
}
