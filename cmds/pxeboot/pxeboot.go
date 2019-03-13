// Copyright 2017-2018 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/u-root/dhcp4/dhcp4client"
	"github.com/u-root/u-root/pkg/boot"
	"github.com/u-root/u-root/pkg/cmdline"
	"github.com/u-root/u-root/pkg/dhclient"
	"github.com/u-root/u-root/pkg/ipxe"
	"github.com/u-root/u-root/pkg/pxe"
	"github.com/vishvananda/netlink"
)

var (
	verbose           = flag.Bool("v", true, "print all kinds of things out, more than Chris wants")
	dryRun            = flag.Bool("dry-run", false, "download kernel, but don't kexec it")
	debug             = func(string, ...interface{}) {}
	removeCmdlineItem = flag.String("remove", "console", "comma separated list of kernel params value to remove from parsed kernel configuration (default to console)")
	reuseCmdlineItem  = flag.String("reuse", "console", "comma separated list of kernel params value to reuse from current kernel (default to console)")
	appendCmdline     = flag.String("append", "", "Additional kernel params")
)

func removeFromCmdline(cl string, fields []string) string {
	var newCl []string

	// kernel variables must allow '-' and '_' to be equivalent in variable
	// names. We will replace dashes with underscores for processing.
	for _, f := range fields {
		f = strings.Replace(f, "-", "_", -1)
	}

	lastQuote := rune(0)
	quotedFieldsCheck := func(c rune) bool {
		switch {
		case c == lastQuote:
			lastQuote = rune(0)
			return false
		case lastQuote != rune(0):
			return false
		case unicode.In(c, unicode.Quotation_Mark):
			lastQuote = c
			return false
		default:
			return unicode.IsSpace(c)
		}
	}

	for _, flag := range strings.FieldsFunc(string(cl), quotedFieldsCheck) {

		// Split the flag into a key and value, setting value="1" if none
		split := strings.Index(flag, "=")

		if len(flag) == 0 {
			continue
		}
		var key string
		if split == -1 {
			key = flag
		} else {
			key = flag[:split]
		}
		canonicalKey := strings.Replace(key, "-", "_", -1)
		skip := false
		for _, f := range fields {
			if canonicalKey == f {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		newCl = append(newCl, flag)
	}
	return strings.Join(newCl, " ")
}

// updateBootCmdline get the kernel command line parameters and append extra
// parameters from the append and reuse flags
func updateBootCmdline(cl string) string {
	acl := ""
	if len(*appendCmdline) > 0 {
		acl = " " + *appendCmdline
	}
	for _, f := range strings.Split(*reuseCmdlineItem, ",") {
		value, present := cmdline.Flag(f)
		if present {
			debug("Cmdline reuse: %s=%v", f, value)
			acl = fmt.Sprintf("%s %s=%s", acl, f, value)
		}
		debug("appendCmdline : '%v'", acl)
	}

	return removeFromCmdline(cl, strings.Split(*removeCmdlineItem, ",")) + acl
}

func attemptDHCPLease(iface netlink.Link, timeout time.Duration, retry int) (*dhclient.Packet4, error) {
	if _, err := dhclient.IfUp(iface.Attrs().Name); err != nil {
		return nil, err
	}

	client, err := dhcp4client.New(iface,
		dhcp4client.WithTimeout(timeout),
		dhcp4client.WithRetry(retry))
	if err != nil {
		return nil, err
	}

	p, err := client.Request()
	if err != nil {
		return nil, err
	}
	return dhclient.NewPacket4(p), nil
}

// getBootImage attempts to parse the file at uri as an ipxe config and returns
// the ipxe boot image. Otherwise falls back to pxe and uses the uri directory,
// ip, and mac address to search for pxe configs.
func getBootImage(uri *url.URL, mac net.HardwareAddr, ip net.IP) (*boot.LinuxImage, error) {
	// Attempt to read the given boot path as an ipxe config file.
	if ipc, err := ipxe.NewConfig(uri); err == nil {
		log.Printf("Got configuration: %s", ipc.BootImage)
		return ipc.BootImage, nil
	}

	// Fallback to pxe boot.
	wd := &url.URL{
		Scheme: uri.Scheme,
		Host:   uri.Host,
		Path:   path.Dir(uri.Path),
	}

	pc := pxe.NewConfig(wd)
	if err := pc.FindConfigFile(mac, ip); err != nil {
		return nil, fmt.Errorf("failed to parse pxelinux config: %v", err)
	}

	label := pc.Entries[pc.DefaultEntry]
	log.Printf("Got configuration: %s", label)
	return label, nil
}

// Netboot tries to boot from eth0 over tftp by parsing pxelinux configuration
func Netboot() error {
	ifs, err := netlink.LinkList()
	if err != nil {
		return err
	}

	for _, iface := range ifs {
		// TODO: Do 'em all in parallel.
		if iface.Attrs().Name != "eth0" {
			continue
		}

		log.Printf("Attempting to get DHCP lease on %s", iface.Attrs().Name)
		packet, err := attemptDHCPLease(iface, 30*time.Second, 5)
		if err != nil {
			log.Printf("No lease on %s: %v", iface.Attrs().Name, err)
			continue
		}
		log.Printf("Got lease on %s", iface.Attrs().Name)
		if err := dhclient.Configure4(iface, packet.P); err != nil {
			log.Printf("shit: %v", err)
			continue
		}

		// We may have to make this DHCPv6 and DHCPv4-specific anyway.
		// Only tested with v4 right now; and assuming the uri points
		// to a pxelinux.0.
		//
		// Or rather, we need to make this option-specific. DHCPv6 has
		// options for passing a kernel and cmdline directly. v4
		// usually just passes a pxelinux.0. But what about an initrd?
		uri, err := packet.Boot()
		if err != nil {
			log.Printf("Got DHCP lease, but no valid PXE information.")
			continue
		}

		log.Printf("Boot URI: %s", uri)

		img, err := getBootImage(uri, iface.Attrs().HardwareAddr, packet.Lease().IP)
		if err != nil {
			return err
		}

		img.Cmdline = updateBootCmdline(img.Cmdline)

		if *dryRun {
			img.ExecutionInfo(log.New(os.Stderr, "", log.LstdFlags))
		} else if err := img.Execute(); err != nil {
			log.Printf("Kexec error: %v", err)
		}
	}
	return nil
}

func main() {
	flag.Parse()
	if *verbose {
		debug = log.Printf
	}

	if err := Netboot(); err != nil {
		log.Fatal(err)
	}
}
