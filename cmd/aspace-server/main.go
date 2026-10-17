// aspace-server serves the shared data repo over HTTP and manages agent keys.
//
//	aspace-server serve --repo /srv/agents-shared-space --keys /etc/aspace/keys.json \
//	                    --db /var/lib/aspace/aspace.db --addr 127.0.0.1:8787
//	aspace-server key add --keys keys.json --name assistant-cloud --read '*' --approve
//	aspace-server key add --keys keys.json --name claude-local --read profile/,items/,agents/
//	aspace-server key add --keys keys.json --name owner --owner
//	aspace-server key list --keys keys.json
//	aspace-server key revoke --keys keys.json --name assistant-cloud   (then restart)
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ketchupz1999/agents-home-public/internal/db"

	"github.com/ketchupz1999/agents-home-public/internal/protocol"
	"github.com/ketchupz1999/agents-home-public/internal/server"
	"github.com/ketchupz1999/agents-home-public/internal/store"
	"github.com/ketchupz1999/agents-home-public/internal/web"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "key":
		key(os.Args[2:])
	case "backup":
		backupDB(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: aspace-server serve|key ...")
	os.Exit(2)
}

// backupDB writes a consistent copy of the database; cron runs this nightly.
func backupDB(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dbc := fs.String("db", "aspace.db", "source database")
	out := fs.String("out", "", "destination file (default: <db>.<timestamp>.bak)")
	keep := fs.Int("keep", 7, "how many recent backups to keep")
	fs.Parse(args)
	d, err := db.Open(*dbc)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()
	if *out == "" {
		*out = fmt.Sprintf("%s.%s.bak", *dbc, time.Now().UTC().Format("20060102T150405Z"))
	}
	if _, err := d.Exec("VACUUM INTO ?", *out); err != nil {
		log.Fatalf("backup: %v", err)
	}
	log.Printf("backup written to %s", *out)
	pruneBackups(*dbc, *keep)
}

func pruneBackups(prefix string, keep int) {
	matches, _ := filepath.Glob(prefix + ".*.bak")
	if len(matches) <= keep {
		return
	}
	sort.Strings(matches)
	for _, m := range matches[:len(matches)-keep] {
		if err := os.Remove(m); err == nil {
			log.Printf("pruned %s", m)
		}
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	repo := fs.String("repo", "", "working tree of the data repo clone")
	keys := fs.String("keys", "keys.json", "key file (kept outside the repo)")
	dbc := fs.String("db", "aspace.db", "SQLite file for mailbox, reads, branch decisions, audit")
	addr := fs.String("addr", "127.0.0.1:8787", "listen address")
	noRemote := fs.Bool("no-remote", false, "do not pull/push (local testing)")
	ttl := fs.Duration("pull-ttl", 10*time.Second, "max staleness for reads")
	noDashboard := fs.Bool("no-dashboard", false, "serve the API only")
	fs.Parse(args)
	if *repo == "" {
		log.Fatal("--repo is required")
	}
	kf, err := server.LoadKeys(*keys)
	if err != nil {
		log.Fatalf("load keys: %v", err)
	}
	st := &store.Store{Dir: *repo, Remote: !*noRemote, PullTTL: *ttl}
	if err := st.Refresh(); err != nil {
		log.Fatalf("initial pull: %v", err)
	}
	d, err := db.Open(*dbc)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	srv := &server.Server{Store: st, Keys: kf, DB: d, Now: time.Now}
	if !*noDashboard {
		srv.Web = &web.Web{Store: st, DB: d, Now: time.Now, SecretKey: kf.OwnerHash(),
			AccessForIdentity: func(id string) (web.IdentityAccess, bool) {
				for _, k := range kf.List() {
					if k.Name == id {
						return web.IdentityAccess{ID: k.Name, Read: append([]string(nil), k.Read...), Owner: k.Owner, Approve: k.CanDecide()}, true
					}
				}
				return web.IdentityAccess{}, false
			},
			Auth: func(secret string) (string, bool) {
				if k := kf.Lookup(secret); k != nil && k.Owner {
					return k.Name, true
				}
				return "", false
			},
			KeyNames: func() []string {
				var names []string
				for _, k := range kf.List() {
					names = append(names, k.Name)
				}
				return names
			}}
		srv.Web.CreateGroup = srv.CreateGroupAs
		srv.Web.UpdateGroupMembers = srv.UpdateGroupMembersAs
	}
	log.Printf("aspace-server listening on %s, repo=%s, %d keys", *addr, *repo, len(kf.List()))
	hs := newHTTPServer(*addr, srv.Handler())
	log.Fatal(hs.ListenAndServe())
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
}

func key(args []string) {
	if len(args) < 1 {
		usage()
	}
	fs := flag.NewFlagSet("key", flag.ExitOnError)
	keys := fs.String("keys", "keys.json", "key file")
	name := fs.String("name", "", "agent name, e.g. muse")
	owner := fs.Bool("owner", false, "owner key: reads everything, may apply branches")
	approve := fs.Bool("approve", false, "may apply/reject agent branches when the owner says so")
	read := fs.String("read", "", "comma-separated path prefixes the agent may read")
	fs.Parse(args[1:])
	kf, err := server.LoadKeys(*keys)
	if err != nil {
		log.Fatal(err)
	}
	switch args[0] {
	case "add":
		if !protocol.ValidName(*name) {
			log.Fatal("--name must be lowercase letters, digits or '-'")
		}
		var prefixes []string
		for _, p := range strings.Split(*read, ",") {
			if p = strings.TrimSpace(p); p != "" {
				prefixes = append(prefixes, p)
			}
		}
		if !*owner && len(prefixes) == 0 {
			log.Fatal("non-owner key needs --read")
		}
		secret, err := kf.Add(*name, *owner, *approve, prefixes)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Fprintf(os.Stderr, "key for %s (shown once):\n", *name)
		fmt.Println(secret)
	case "revoke":
		found, err := kf.Revoke(*name)
		if err != nil {
			log.Fatal(err)
		}
		if !found {
			log.Fatalf("no key named %q", *name)
		}
		fmt.Fprintf(os.Stderr, "revoked %s; restart the server to apply\n", *name)
	case "list":
		for _, k := range kf.List() {
			fmt.Printf("%-16s owner=%-5v approve=%-5v read=%s\n", k.Name, k.Owner, k.Approve, strings.Join(k.Read, ","))
		}
	default:
		usage()
	}
}
