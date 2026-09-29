package api

import (
	"fmt"
	"path/filepath"
	"strings"
)

// GrantConflict reports the first part of grant that overlaps a deny in p. A
// deny is a hard floor: no approval widens access into it, whether the grant
// names the denied path, a path beneath it, or a parent that contains it.
func (p NativeSandboxPolicy) GrantConflict(grant NativeSandboxPolicy) error {
	if fs, granted := p.Filesystem, grant.Filesystem; fs != nil && granted != nil {
		if err := rootsConflict("write", granted.WritableRoots, append(append([]string{}, fs.DeniedWriteRoots...), fs.DeniedReadRoots...)); err != nil {
			return err
		}
		if err := rootsConflict("read", granted.ReadableRoots, fs.DeniedReadRoots); err != nil {
			return err
		}
	}
	if network, granted := p.Network, grant.Network; network != nil && granted != nil {
		for _, domain := range granted.AllowedDomains {
			for _, denied := range network.DeniedDomains {
				if domainMatches(denied, domain) {
					return fmt.Errorf("granting domain %q overlaps denied domain %q", domain, denied)
				}
			}
		}
	}
	return nil
}

func rootsConflict(access string, granted, denied []string) error {
	for _, root := range granted {
		for _, deny := range denied {
			if pathContains(deny, root) || pathContains(root, deny) {
				return fmt.Errorf("granting %s access to %q overlaps denied root %q", access, root, deny)
			}
		}
	}
	return nil
}

// pathContains reports whether child is parent or lies beneath it.
func pathContains(parent, child string) bool {
	parent, child = filepath.Clean(parent), filepath.Clean(child)
	if parent == child {
		return true
	}
	return strings.HasPrefix(child, strings.TrimSuffix(parent, string(filepath.Separator))+string(filepath.Separator))
}

func domainMatches(pattern, domain string) bool {
	pattern, domain = strings.ToLower(pattern), strings.ToLower(domain)
	if suffix, wildcard := strings.CutPrefix(pattern, "*."); wildcard {
		return domain == suffix || strings.HasSuffix(domain, "."+suffix)
	}
	return pattern == domain
}
