package provider

import (
	"fmt"
	"log"
	"strings"

	"github.com/NX211/traefik-proxmox-provider/dynamic"
	"github.com/NX211/traefik-proxmox-provider/internal"
	"github.com/traefik/paerser/parser"
)

// creates the final dynamic configuration by processing all discovered services and their labels
func generateConfiguration(servicesMap map[string][]internal.Service) *dynamic.Configuration {
	config := &dynamic.Configuration{
		HTTP: &dynamic.HTTPConfiguration{
			Routers:     make(map[string]*dynamic.Router),
			Services:    make(map[string]*dynamic.Service),
			Middlewares: make(map[string]*dynamic.Middleware),
		},
		TCP: &dynamic.TCPConfiguration{
			Routers:  make(map[string]*dynamic.TCPRouter),
			Services: make(map[string]*dynamic.TCPService),
		},
		UDP: &dynamic.UDPConfiguration{
			Routers:  make(map[string]*dynamic.UDPRouter),
			Services: make(map[string]*dynamic.UDPService),
		},
	}

	for nodeName, services := range servicesMap {
		for _, service := range services {
			log.Printf("Processing service %s (ID: %d) on node %s", service.Name, service.ID, nodeName)

			// Populate all user-defined configuration from labels
			err := parser.Decode(service.Config, config, "traefik", "traefik.http", "traefik.tcp", "traefik.udp")
			if err != nil {
				log.Printf("ERROR: Could not decode labels for service %s: %v", service.Name, err)
				continue
			}

			// Build defaults and enrich configurations for each protocol.
			buildHTTPConfiguration(config.HTTP, service, nodeName)
			buildTCPConfiguration(config.TCP, service, nodeName)
			buildUDPConfiguration(config.UDP, service, nodeName)
		}
	}

	return config
}

// buildHTTPConfiguration creates default HTTP routers/services and enriches existing ones.
func buildHTTPConfiguration(httpConfig *dynamic.HTTPConfiguration, service internal.Service, nodeName string) {
	defaultID := fmt.Sprintf("%s-%d", service.Name, service.ID)
	definedRouters := getDefinedElements(service.Config, "http", "routers")
	definedServices := getDefinedElements(service.Config, "http", "services")

	// Create a default router if none are defined in labels for this service.
	if len(definedRouters) == 0 {
		httpConfig.Routers[defaultID] = &dynamic.Router{}
		definedRouters = append(definedRouters, defaultID)
	}

	// Create a default service if none are defined.
	if len(definedServices) == 0 {
		httpConfig.Services[defaultID] = &dynamic.Service{}
		definedServices = append(definedServices, defaultID)
	}

	// Enrich all routers associated with this service.
	for _, routerName := range definedRouters {
		router := httpConfig.Routers[routerName]
		if router == nil {
			continue
		}

		// If the user defined a router but no service, link to the first available service.
		if router.Service == "" && len(definedServices) > 0 {
			router.Service = definedServices[0]
		}

		// Provide a default rule if none is set
		if router.Rule == "" {
			router.Rule = fmt.Sprintf("Host(`%s`)", service.Name)
		}

		// Set default priority if not set
		if router.Priority == nil {
			defaultPriority := 1
			router.Priority = &defaultPriority
		}
	}

	// Enrich all services associated with this service.
	for _, serviceName := range definedServices {
		configService := httpConfig.Services[serviceName]
		if configService == nil {
			continue
		}

		if configService.LoadBalancer == nil {
			configService.LoadBalancer = &dynamic.ServersLoadBalancer{}
		}
		if configService.LoadBalancer.PassHostHeader == nil {
			configService.LoadBalancer.PassHostHeader = new(bool)
			*configService.LoadBalancer.PassHostHeader = true
		}
		if len(configService.LoadBalancer.Servers) == 0 {
			configService.LoadBalancer.Servers = []dynamic.Server{{}}
		}

		// Fill in the URL for any server that doesn't have one.
		for i := range configService.LoadBalancer.Servers {
			server := &configService.LoadBalancer.Servers[i]
			if server.URL == "" {
				server.URL = buildServerURL(service, server, nodeName)
			}
		}
	}
}

// buildTCPConfiguration enriches TCP routers and services defined in labels.
func buildTCPConfiguration(tcpConfig *dynamic.TCPConfiguration, service internal.Service, nodeName string) {
	defaultID := fmt.Sprintf("%s-%d", service.Name, service.ID)

	definedRouters := getDefinedElements(service.Config, "tcp", "routers")
	definedServices := getDefinedElements(service.Config, "tcp", "services")

	// Create a default service if there are TCP routers but no services defined in labels.
	if len(definedRouters) > 0 && len(definedServices) == 0 {
		tcpConfig.Services[defaultID] = &dynamic.TCPService{}
		definedServices = append(definedServices, defaultID)
	}

	for _, routerName := range definedRouters {
		router := tcpConfig.Routers[routerName]
		if router == nil {
			continue
		}

		// If the user defined a router but no service, link to the first available service.
		if router.Service == "" && len(definedServices) > 0 {
			router.Service = definedServices[0]
		}

		// Set default priority if not set
		if router.Priority == nil {
			defaultPriority := 1
			router.Priority = &defaultPriority
		}

		// Provide a default rule if none is set.
		if router.Rule == "" {
			router.Rule = "HostSNI(`*`)"
		}
	}

	for _, serviceName := range definedServices {
		configService := tcpConfig.Services[serviceName]
		if configService == nil {
			continue
		}

		if configService.LoadBalancer == nil {
			configService.LoadBalancer = &dynamic.TCPServersLoadBalancer{}
		}

		if len(configService.LoadBalancer.Servers) == 0 {
			configService.LoadBalancer.Servers = []dynamic.TCPServer{{}}
		}

		// Fill in the Address for any tcp server that doesn't have one.
		for i := range configService.LoadBalancer.Servers {
			server := &configService.LoadBalancer.Servers[i]
			if server.Address == "" {
				if server.Port == "" {
					log.Printf("WARNING: TCP server for service %s has no port defined. Skipping address construction.", service.Name)
					continue
				}

				server.Address = buildStreamServerAddress(service, nodeName, server.Port)
			}
		}
	}
}

// buildUDPConfiguration enriches UDP routers and services defined in labels.
func buildUDPConfiguration(udpConfig *dynamic.UDPConfiguration, service internal.Service, nodeName string) {
	defaultID := fmt.Sprintf("%s-%d", service.Name, service.ID)

	definedRouters := getDefinedElements(service.Config, "udp", "routers")
	definedServices := getDefinedElements(service.Config, "udp", "services")

	// Create a default service if there are UDP routers but no services defined in labels.
	if len(definedRouters) > 0 && len(definedServices) == 0 {
		udpConfig.Services[defaultID] = &dynamic.UDPService{}
		definedServices = append(definedServices, defaultID)
	}

	for _, routerName := range definedRouters {
		router := udpConfig.Routers[routerName]
		if router == nil {
			continue
		}

		// If the user defined a router but no service, link to the first available service.
		if router.Service == "" && len(definedServices) > 0 {
			router.Service = definedServices[0]
		}
	}

	for _, serviceName := range definedServices {
		configService := udpConfig.Services[serviceName]
		if configService == nil {
			continue
		}

		if configService.LoadBalancer == nil {
			configService.LoadBalancer = &dynamic.UDPServersLoadBalancer{}
		}

		if len(configService.LoadBalancer.Servers) == 0 {
			configService.LoadBalancer.Servers = []dynamic.UDPServer{{}}
		}

		// Fill in the Address for any tcp server that doesn't have one.
		for i := range configService.LoadBalancer.Servers {
			server := &configService.LoadBalancer.Servers[i]
			if server.Address == "" {
				if server.Port == "" {
					log.Printf("WARNING: UDP server for service %s has no port defined. Skipping address construction.", service.Name)
					continue
				}
				server.Address = buildStreamServerAddress(service, nodeName, server.Port)
			}
		}
	}
}

// buildServerURL constructs the final URL for an HTTP server.
func buildServerURL(service internal.Service, server *dynamic.Server, nodeName string) string {
	scheme := "http"
	port := "80"

	// User-defined scheme from labels takes precedence.
	if server.Scheme == "https" {
		scheme = "https"
		port = "443"
	}

	if server.Port != "" {
		port = server.Port
	}

	ip := getServiceIP(service, nodeName)
	return fmt.Sprintf("%s://%s:%s", scheme, ip, port)
}

// buildStreamServerAddress constructs the final address for a TCP or UDP server.
func buildStreamServerAddress(service internal.Service, nodeName string, port string) string {
	ip := getServiceIP(service, nodeName)
	return fmt.Sprintf("%s:%s", ip, port)
}

// getServiceIP finds the best IP address for a service, falling back to hostname.
func getServiceIP(service internal.Service, nodeName string) string {
	// Use the first valid IP from the guest agent.
	for _, ip := range service.IPs {
		if ip.Address != "" && ip.Address != "127.0.0.1" {
			return ip.Address
		}
	}
	// Fall back to a DNS-resolvable name.
	log.Printf("WARNING: No valid IP found for service %s via guest agent. Falling back to hostname '%s.%s'. Ensure DNS is configured.", service.Name, service.Name, nodeName)
	return fmt.Sprintf("%s.%s", service.Name, nodeName)
}

// getDefinedElements finds all uniquely named routers or services from labels.
func getDefinedElements(labels map[string]string, proto, elemType string) []string {
	prefix := fmt.Sprintf("traefik.%s.%s.", proto, elemType)
	keys := make(map[string]struct{})
	for k := range labels {
		if strings.HasPrefix(k, prefix) {
			parts := strings.Split(k[len(prefix):], ".")
			if len(parts) > 0 {
				keys[parts[0]] = struct{}{}
			}
		}
	}

	var uniqueKeys []string
	for k := range keys {
		uniqueKeys = append(uniqueKeys, k)
	}
	return uniqueKeys
}
