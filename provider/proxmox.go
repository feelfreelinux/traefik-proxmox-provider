package provider

import (
	"context"
	"fmt"
	"log"

	"github.com/NX211/traefik-proxmox-provider/internal"
)

func newClient(pc ParserConfig) *internal.ProxmoxClient {
	return internal.NewProxmoxClient(pc.ApiEndpoint, pc.TokenId, pc.Token, pc.ValidateSSL, pc.LogLevel)
}

func logVersion(client *internal.ProxmoxClient, ctx context.Context) error {
	version, err := client.GetVersion(ctx)
	if err != nil {
		return err
	}
	log.Printf("Connected to Proxmox VE version %s", version.Release)
	return nil
}

func getServiceMap(client *internal.ProxmoxClient, ctx context.Context) (map[string][]internal.Service, error) {
	servicesMap := make(map[string][]internal.Service)

	nodes, err := client.GetNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("error scanning nodes: %w", err)
	}

	for _, nodeStatus := range nodes {
		services, err := scanServices(client, ctx, nodeStatus.Node)
		if err != nil {
			log.Printf("Error scanning services on node %s: %v", nodeStatus.Node, err)
			continue
		}
		servicesMap[nodeStatus.Node] = services
	}
	return servicesMap, nil
}

func getIPsOfService(client *internal.ProxmoxClient, ctx context.Context, nodeName string, vmID uint64, isContainer bool) (ips []internal.IP, err error) {
	var agentInterfaces *internal.ParsedAgentInterfaces
	if isContainer {
		agentInterfaces, err = client.GetContainerNetworkInterfaces(ctx, nodeName, vmID)
		if err != nil {
			log.Printf("DEBUG: Error getting container network interfaces for %s/%d: %v", nodeName, vmID, err)
			return nil, fmt.Errorf("error getting container network interfaces: %w", err)
		}
	} else {
		agentInterfaces, err = client.GetVMNetworkInterfaces(ctx, nodeName, vmID)
		if err != nil {
			log.Printf("DEBUG: Error getting VM network interfaces for %s/%d: %v", nodeName, vmID, err)
			return nil, fmt.Errorf("error getting VM network interfaces: %w", err)
		}
	}

	rawIPs := agentInterfaces.GetIPs()

	filteredIPs := make([]internal.IP, 0)
	for _, ip := range rawIPs {
		if (ip.AddressType == "ipv4" || ip.AddressType == "inet") && ip.Address != "127.0.0.1" {
			filteredIPs = append(filteredIPs, ip)
		}
	}

	if len(filteredIPs) == 0 && client.LogLevel == internal.LogLevelDebug {
		log.Printf("DEBUG: No valid IPs found for %s/%d (isContainer: %t). Raw IPs were: %+v", nodeName, vmID, isContainer, rawIPs)
	}

	return filteredIPs, nil
}

func scanServices(client *internal.ProxmoxClient, ctx context.Context, nodeName string) (services []internal.Service, err error) {
	// Scan virtual machines
	vms, err := client.GetVirtualMachines(ctx, nodeName)
	if err != nil {
		return nil, fmt.Errorf("error scanning VMs on node %s: %w", nodeName, err)
	}

	for _, vm := range vms {
		log.Printf("Scanning VM %s/%s (%d): %s", nodeName, vm.Name, vm.VMID, vm.Status)

		if vm.Status == "running" {
			config, err := client.GetVMConfig(ctx, nodeName, vm.VMID)
			if err != nil {
				log.Printf("Error getting VM config for %d: %v", vm.VMID, err)
				continue
			}

			configMap := config.GetTraefikMap()

			if configMap["traefik.enable"] != "true" {
				log.Printf("Skipping VM %s (%d) because traefik.enable is not true", vm.Name, vm.VMID)
			}

			log.Printf("VM %s (%d) traefik config: %v", vm.Name, vm.VMID, configMap)

			service := internal.NewService(vm.VMID, vm.Name, configMap)

			ips, err := getIPsOfService(client, ctx, nodeName, vm.VMID, false)
			if err == nil {
				service.IPs = ips
			}

			services = append(services, service)
		}
	}

	// Scan containers
	cts, err := client.GetContainers(ctx, nodeName)
	if err != nil {
		return nil, fmt.Errorf("error scanning containers on node %s: %w", nodeName, err)
	}

	for _, ct := range cts {
		log.Printf("Scanning container %s/%s (%d): %s", nodeName, ct.Name, ct.VMID, ct.Status)

		if ct.Status == "running" {
			config, err := client.GetContainerConfig(ctx, nodeName, ct.VMID)
			if err != nil {
				log.Printf("Error getting container config for %d: %v", ct.VMID, err)
				continue
			}

			configMap := config.GetTraefikMap()

			if configMap["traefik.enable"] != "true" {
				log.Printf("Skipping container %s (%d) because traefik.enable is not true", ct.Name, ct.VMID)
				continue
			}

			log.Printf("Container %s (%d) traefik config: %v", ct.Name, ct.VMID, configMap)

			service := internal.NewService(ct.VMID, ct.Name, configMap)

			// Try to get container IPs if possible
			ips, err := getIPsOfService(client, ctx, nodeName, ct.VMID, true)
			if err == nil {
				service.IPs = ips
			}

			services = append(services, service)
		}
	}

	return services, nil
}
