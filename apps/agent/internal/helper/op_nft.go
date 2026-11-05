package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"vpnmap/pkg/protocol"
	"vpnmap/pkg/sysexec"
)

// nftCounters lists the named counters of table inet vpnmap. Only that table
// is ever named on the command line, so no other ruleset is read.
func (s *Server) nftCounters(ctx context.Context) ([]protocol.NftCounter, error) {
	res, err := s.Runner.Run(ctx, sysexec.Cmd{
		Name: "nft",
		Args: []string{"-j", "list", "counters", "table", "inet", "vpnmap"},
	})
	if err != nil {
		if ee, ok := err.(*sysexec.ExitError); ok && strings.Contains(ee.Stderr, "No such file") {
			return nil, fmt.Errorf("table inet vpnmap does not exist (vpnmap-agent nft-apply has not run)")
		}
		return nil, err
	}
	return ParseNftCounters(res.Stdout)
}

// ParseNftCounters parses `nft -j list counters table inet vpnmap`:
//
//	{"nftables":[{"metainfo":{...}},{"counter":{"family":"inet","name":"x",
//	  "table":"vpnmap","handle":3,"packets":10,"bytes":1200}}]}
func ParseNftCounters(b []byte) ([]protocol.NftCounter, error) {
	var doc struct {
		Nftables []struct {
			Counter *struct {
				Family  string `json:"family"`
				Name    string `json:"name"`
				Table   string `json:"table"`
				Packets uint64 `json:"packets"`
				Bytes   uint64 `json:"bytes"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("cannot parse nft JSON output")
	}
	out := []protocol.NftCounter{}
	for _, e := range doc.Nftables {
		c := e.Counter
		if c == nil || c.Family != "inet" || c.Table != "vpnmap" {
			continue
		}
		out = append(out, protocol.NftCounter{Name: c.Name, Bytes: c.Bytes, Packets: c.Packets})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
