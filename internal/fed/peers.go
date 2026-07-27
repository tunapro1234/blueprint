package fed

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func PeersPath(stateDir string) string {
	return filepath.Join(stateDir, "fed", "peers.json")
}

func LoadPeers(stateDir string) (map[string]Peer, error) {
	return loadPeers(stateDir, os.Stderr)
}

func loadPeers(stateDir string, log io.Writer) (map[string]Peer, error) {
	path := PeersPath(stateDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	peers := map[string]Peer{}
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	loaded := make(map[string]Peer, len(peers))
	tokenOwners := make(map[string]string, len(peers))
	for name, peer := range peers {
		if err := validateName(name); err != nil {
			logPeerSkip(log, name, err)
			continue
		}
		validExpose := true
		for _, exposedName := range peer.Expose {
			if err := validateName(exposedName); err != nil {
				logPeerSkip(log, name, fmt.Errorf("invalid expose entry %q: %w", exposedName, err))
				validExpose = false
				break
			}
		}
		if !validExpose {
			continue
		}
		if len(peer.Token) != 64 {
			return nil, fmt.Errorf("parse %s: peer %q token must be 64 hexadecimal characters", path, name)
		}
		if _, err := hex.DecodeString(peer.Token); err != nil {
			return nil, fmt.Errorf("parse %s: peer %q token must be 64 hexadecimal characters", path, name)
		}
		normalized := strings.ToLower(peer.Token)
		if owner, exists := tokenOwners[normalized]; exists {
			return nil, fmt.Errorf("parse %s: duplicate token for peers %q and %q", path, owner, name)
		}
		tokenOwners[normalized] = name
		loaded[name] = peer
	}
	return loaded, nil
}

func logPeerSkip(log io.Writer, name string, err error) {
	if log != nil {
		fmt.Fprintf(log, "fed: skipping peer %q: %v\n", name, err)
	}
}

func PeerNames(peers map[string]Peer) []string {
	names := make([]string, 0, len(peers))
	for name := range peers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func GenerateToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func exposed(peer Peer, target string) bool {
	for _, name := range peer.Expose {
		if name == target {
			return true
		}
	}
	return false
}

func validHexToken(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return false
		}
	}
	return true
}
