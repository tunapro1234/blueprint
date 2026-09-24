package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	bpconfig "blueprint/internal/config"
	"blueprint/internal/identity"
)

type pathLookup func(string) (string, error)

func execLookPath(name string) (string, error) { return exec.LookPath(name) }

func remoteDestination(remote bpconfig.RemoteConfig) string {
	host := remoteHostTarget(remote.Host)
	if remote.User != "" {
		return remote.User + "@" + host
	}
	return host
}

func remoteHostTarget(host string) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func remoteBPCommand(remote bpconfig.RemoteConfig, bpArgs []string, lookup pathLookup) (commandSpec, error) {
	command := append(strings.Fields(remote.Elevate), "bp")
	command = append(command, bpArgs...)
	return remoteTransportCommand(remote, command, lookup)
}

func remoteShellCommand(remote bpconfig.RemoteConfig, lookup pathLookup) (commandSpec, error) {
	return remoteTransportCommand(remote, strings.Fields(remote.Elevate), lookup)
}

func remoteTransportCommand(remote bpconfig.RemoteConfig, command []string, lookup pathLookup) (commandSpec, error) {
	transport := remote.Transport
	if transport == "" {
		transport = "ssh"
	}
	path, err := lookup(transport)
	if err != nil {
		return commandSpec{}, fmt.Errorf("%s is not installed", transport)
	}
	destination := remoteDestination(remote)
	if transport == "ssh" {
		args := []string{"ssh", "-t"}
		if remote.Port != 0 {
			args = append(args, "-p", strconv.Itoa(remote.Port))
		}
		if remote.Identity != "" {
			args = append(args, "-i", remote.Identity)
		}
		args = append(args, destination)
		args = append(args, command...)
		return commandSpec{Path: path, Args: args}, nil
	}
	if transport != "mosh" {
		return commandSpec{}, fmt.Errorf("unsupported remote transport: %s", transport)
	}
	args := []string{"mosh"}
	if remote.Port != 0 || remote.Identity != "" {
		sshCommand := "ssh"
		if remote.Port != 0 {
			sshCommand += " -p " + strconv.Itoa(remote.Port)
		}
		if remote.Identity != "" {
			sshCommand += " -i " + quoteShell(remote.Identity)
		}
		args = append(args, "--ssh="+sshCommand)
	}
	if remote.MoshPorts != "" {
		args = append(args, "--port="+remote.MoshPorts)
	}
	args = append(args, destination)
	if len(command) > 0 {
		args = append(args, command...)
	}
	return commandSpec{Path: path, Args: args}, nil
}

func (a *app) remoteRegistry(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "list":
		if len(args) > 2 || len(args) == 2 && args[1] != "--json" {
			return true, fmt.Errorf("usage: bp remote list [--json]")
		}
		if len(args) == 2 {
			remotes := a.config.Remotes
			if remotes == nil {
				remotes = map[string]bpconfig.RemoteConfig{}
			}
			return true, json.NewEncoder(a.out).Encode(remotes)
		}
		names := make([]string, 0, len(a.config.Remotes))
		for name := range a.config.Remotes {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			fmt.Fprintln(a.out, "(no remotes)")
			return true, nil
		}
		for _, name := range names {
			remote := a.config.Remotes[name]
			transport := remote.Transport
			if transport == "" {
				transport = "ssh"
			}
			fmt.Fprintf(a.out, "%s\t%s\t%s\n", name, transport, remoteDestination(remote))
		}
		return true, nil
	case "add":
		if len(args) < 2 {
			return true, fmt.Errorf("usage: bp remote add <name> --host <host> [--port N] [--user <user>] [--identity <path>] [--transport mosh|ssh] [--mosh-ports <range>] [--elevate <command>]")
		}
		name := args[1]
		if !identity.ValidName(name) {
			return true, fmt.Errorf("invalid remote name: %s", name)
		}
		remote := bpconfig.RemoteConfig{Transport: "ssh"}
		seen := map[string]bool{}
		for index := 2; index < len(args); index++ {
			flag := args[index]
			if !strings.HasPrefix(flag, "--") || index+1 >= len(args) {
				return true, fmt.Errorf("invalid remote add option: %s", flag)
			}
			if seen[flag] {
				return true, fmt.Errorf("%s may only be specified once", flag)
			}
			seen[flag] = true
			index++
			value := args[index]
			switch flag {
			case "--host":
				remote.Host = value
			case "--port":
				port, err := strconv.Atoi(value)
				if err != nil {
					return true, fmt.Errorf("invalid remote port: %s", value)
				}
				remote.Port = port
			case "--user":
				remote.User = value
			case "--identity":
				remote.Identity = value
			case "--transport":
				remote.Transport = value
			case "--mosh-ports":
				remote.MoshPorts = value
			case "--elevate":
				remote.Elevate = value
			default:
				return true, fmt.Errorf("unknown remote add option: %s", flag)
			}
		}
		if remote.Host == "" {
			return true, fmt.Errorf("--host is required")
		}
		path := a.config.Path
		if path == "" {
			var err error
			path, err = bpconfig.InitYAML(a.config.Home)
			if err != nil {
				return true, err
			}
		}
		if err := bpconfig.UpdateRemote(path, name, &remote); err != nil {
			return true, err
		}
		fmt.Fprintf(a.out, "remote %s saved in %s\n", name, path)
		return true, nil
	case "rm":
		if len(args) != 2 {
			return true, fmt.Errorf("usage: bp remote rm <name>")
		}
		if a.config.Path == "" {
			return true, fmt.Errorf("unknown remote: %s", args[1])
		}
		if _, ok := a.config.Remotes[args[1]]; !ok {
			return true, fmt.Errorf("unknown remote: %s", args[1])
		}
		if err := bpconfig.UpdateRemote(a.config.Path, args[1], nil); err != nil {
			return true, err
		}
		fmt.Fprintf(a.out, "remote %s removed from %s\n", args[1], a.config.Path)
		return true, nil
	default:
		return false, nil
	}
}

func (a *app) shell(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: bp shell <server> [agent]")
	}
	remote, ok := a.config.Remotes[args[0]]
	if !ok {
		return fmt.Errorf("unknown remote: %s", args[0])
	}
	var spec commandSpec
	var err error
	if len(args) == 2 {
		if !identity.ValidName(args[1]) {
			return fmt.Errorf("invalid agent name: %s", args[1])
		}
		spec, err = remoteBPCommand(remote, []string{"attach", args[1]}, execLookPath)
	} else {
		spec, err = remoteShellCommand(remote, execLookPath)
	}
	if err != nil {
		return err
	}
	return replaceWith(spec)
}
