/*
 * Teleport
 * Copyright (C) 2023  Gravitational, Inc.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package server

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/gravitational/trace"

	usageeventsv1 "github.com/gravitational/teleport/api/gen/proto/go/usageevents/v1"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/api/types/installers"
	"github.com/gravitational/teleport/api/utils"
	"github.com/gravitational/teleport/lib/cloud/azure"
	"github.com/gravitational/teleport/lib/services"
)

const azureEventPrefix = "azure/"

// maxPowerStateFallbackLookupsPerFetch caps per-VM calls when VMs are missing from the bulk StatusOnly response.
// Without a cap, an incomplete bulk response would cause O(N) individual ARM calls, the same amplification the bulk
// path is designed to avoid. VMs beyond the cap are passed through without a power-state check (fail-open).
const maxPowerStateFallbackLookupsPerFetch = 10

// vmPowerStates fetches VM power states once per poll cycle for a given (integration, subscription)
// group. The fetch is deferred until the first caller invokes get, and subsequent callers receive
// the cached result. This is safe for both sequential and concurrent use via sync.Once.
//
// Instances are created fresh each time fetchers are rebuilt (every poll cycle via fullRefresh),
// so results never go stale across cycles.
type vmPowerStates struct {
	once   sync.Once
	states map[string]azure.PowerState
	err    error
}

// get returns the power states map. The underlying API call executes at most once; all subsequent
// calls return the same (result, error), including errors — a failed fetch is not retried until a new
// vmPowerStates is created next poll cycle. The returned map is shared and must not be modified by callers.
// The first caller's context is used for the API call. All callers in a poll cycle share the discovery
// server's context, so this is safe. If per-fetcher timeouts are ever introduced, this must be revisited.
func (p *vmPowerStates) get(ctx context.Context, client azure.VirtualMachinesClient) (map[string]azure.PowerState, error) {
	p.once.Do(func() {
		p.states, p.err = client.ListVirtualMachineStatuses(ctx, types.Wildcard)
	})
	return p.states, p.err
}

// AzureInstances contains information about discovered Azure virtual machines.
type AzureInstances struct {
	// DiscoveryConfigName is the name of discovery config.
	DiscoveryConfigName string
	// Integration is the optional name of the integration to use for auth.
	Integration string

	// Region is the Azure region where the instances are located.
	Region string
	// SubscriptionID is the subscription ID for the instances.
	SubscriptionID string
	// ResourceGroup is the resource group for the instances.
	ResourceGroup string

	// InstallerParams are the installer parameters used for installation.
	InstallerParams *types.InstallerParams
	// Instances is a list of discovered Azure virtual machines.
	Instances []*armcompute.VirtualMachine
}

// AzureDiscoveryFetcher exposes Azure-specific fetcher metadata used for observability and grouping.
type AzureDiscoveryFetcher interface {
	Fetcher[*AzureInstances]
	GetSubscription() string
	GetResourceGroup() string
}

// MakeEvents generates MakeEvents for these instances.
func (instances *AzureInstances) MakeEvents(failures []AzureInstallFailure) map[string]*usageeventsv1.ResourceCreateEvent {
	resourceType := types.DiscoveredResourceNode
	if instances.InstallerParams != nil && instances.InstallerParams.ScriptName == installers.InstallerScriptNameAgentless {
		resourceType = types.DiscoveredResourceAgentlessNode
	}

	failed := map[string]struct{}{}
	for _, failure := range failures {
		id := azure.StringVal(failure.Instance.ID)
		failed[id] = struct{}{}
	}

	expectedSize := len(instances.Instances) - len(failures)
	events := make(map[string]*usageeventsv1.ResourceCreateEvent, expectedSize)
	for _, inst := range instances.Instances {
		id := azure.StringVal(inst.ID)
		// skip failed
		if _, found := failed[id]; found {
			continue
		}
		events[azureEventPrefix+id] = &usageeventsv1.ResourceCreateEvent{
			ResourceType:        resourceType,
			ResourceOrigin:      types.OriginCloud,
			CloudProvider:       types.CloudAzure,
			DiscoveryConfigName: instances.DiscoveryConfigName,
		}
	}
	return events
}

// FilterExistingNodes removes instances matching existing nodes in place.
func (instances *AzureInstances) FilterExistingNodes(existingNodes []types.Server) {
	vmIDs := make(map[string]struct{})
	for _, node := range existingNodes {
		labels := node.GetAllLabels()
		subscriptionID := labels[types.SubscriptionIDLabelInternal]
		if subscriptionID != instances.SubscriptionID {
			continue
		}
		vmID := labels[types.VMIDLabelInternal]
		if vmID != "" {
			vmIDs[vmID] = struct{}{}
		}
	}

	instances.Instances = slices.DeleteFunc(instances.Instances, func(instance *armcompute.VirtualMachine) bool {
		var vmID string
		if instance.Properties != nil && instance.Properties.VMID != nil {
			vmID = *instance.Properties.VMID
		}
		_, found := vmIDs[vmID]
		return found
	})
}

type azureClientGetter func(ctx context.Context, integration string) (azure.Clients, error)

type listSubscriptionsFunc func(ctx context.Context, integration string) (subscriptions []string, err error)

// MatchersToAzureInstanceFetchers converts a list of Azure VM Matchers into a list of Azure VM Fetchers.
func MatchersToAzureInstanceFetchers(
	ctx context.Context,
	logger *slog.Logger,
	matchers []types.AzureMatcher,
	getClient azureClientGetter,
	discoveryConfigName string,
	listSubs listSubscriptionsFunc,
) []Fetcher[*AzureInstances] {
	ret := make([]Fetcher[*AzureInstances], 0)
	for _, matcher := range matchers {
		matcher.Subscriptions = expandAzureMatcherSubscriptions(ctx, logger, matcher.Subscriptions, matcher.Integration, listSubs)
		for _, subscription := range matcher.Subscriptions {
			for _, resourceGroup := range matcher.ResourceGroups {
				fetcher := newAzureInstanceFetcher(azureFetcherConfig{
					Matcher:             matcher,
					Subscription:        subscription,
					ResourceGroup:       resourceGroup,
					AzureClientGetter:   getClient,
					DiscoveryConfigName: discoveryConfigName,
					Logger:              logger,
				})
				ret = append(ret, fetcher)
			}
		}
	}
	return ret
}

// shareVMPowerStates assigns a shared vmPowerStates to
// each group of wildcard fetchers that share the same (integration,
// subscription) key. Within each group, all fetchers receive the
// same vmPowerStates instance, so the subscription-wide
// ListVirtualMachineStatuses call executes at most once per poll
// cycle regardless of how many matchers exist for that key.
//
// Non-wildcard fetchers keep a nil vmPowerStates — they skip
// power-state filtering because the Azure API does not support
// StatusOnly for per-RG listings, and per-VM Get calls would
// create O(N) ARM amplification.
func shareVMPowerStates(
	ctx context.Context,
	logger *slog.Logger,
	fetchers []Fetcher[*AzureInstances],
) {
	type key struct {
		integration  string
		subscription string
	}

	byKey := make(map[key]*vmPowerStates)
	for _, f := range fetchers {
		azureFetcher, ok := f.(*azureInstanceFetcher)
		if !ok || azureFetcher.ResourceGroup != types.Wildcard {
			continue
		}

		k := key{
			integration:  azureFetcher.Integration,
			subscription: azureFetcher.Subscription,
		}
		if _, exists := byKey[k]; !exists {
			byKey[k] = &vmPowerStates{}
		}
		azureFetcher.vmPowerStates = byKey[k]
	}

	// Log when multiple fetchers share a single vmPowerStates —
	// useful for operators to understand API call deduplication.
	for k, shared := range byKey {
		var count int
		for _, f := range fetchers {
			af, ok := f.(*azureInstanceFetcher)
			if !ok {
				continue
			}
			if af.vmPowerStates == shared {
				count++
			}
		}
		if count > 1 {
			logger.InfoContext(ctx,
				"Azure VM power-state lookup shared across wildcard fetchers",
				"integration", k.integration,
				"subscription_id", k.subscription,
				"wildcard_fetchers", count,
			)
		}
	}
}

// ShareAzureVMPowerStates rewires wildcard Azure fetchers to use
// shared vmPowerStates grouped by (integration, subscription).
//
// Callers that build fetchers across multiple independent batches should
// invoke this once with the combined fetcher set to avoid duplicated
// subscription-wide ListAll(StatusOnly) scans.
func ShareAzureVMPowerStates(
	ctx context.Context,
	logger *slog.Logger,
	fetchers []Fetcher[*AzureInstances],
) {
	shareVMPowerStates(ctx, logger, fetchers)
}

// expandAzureMatcherSubscriptions fetches the subscriptions for any wildcard
// subscriptions and replaces the wildcard with the subscriptions list.
func expandAzureMatcherSubscriptions(
	ctx context.Context,
	logger *slog.Logger,
	subscriptions []string,
	integration string,
	listSubs listSubscriptionsFunc,
) []string {
	var out []string
	for _, sub := range subscriptions {
		if sub != types.Wildcard {
			out = append(out, sub)
			continue
		}
		subs, err := listSubs(ctx, integration)
		if err != nil {
			// TODO(gavin): make a user task
			logger.WarnContext(ctx, "Failed to fetch Azure subscription list for wildcard in discovery configuration",
				"integration", integration,
				"error", err,
			)
			continue
		}
		out = append(out, subs...)
	}
	return utils.Deduplicate(out)
}

type azureFetcherConfig struct {
	Matcher             types.AzureMatcher
	Subscription        string
	ResourceGroup       string
	AzureClientGetter   azureClientGetter
	DiscoveryConfigName string
	Logger              *slog.Logger
}

type azureInstanceFetcher struct {
	InstallerParams     *types.InstallerParams
	AzureClientGetter   azureClientGetter
	Regions             []string
	Subscription        string
	ResourceGroup       string
	Labels              types.Labels
	DiscoveryConfigName string
	Integration         string
	Logger              *slog.Logger
	// vmPowerStates holds the shared, lazily-fetched power states
	// for this fetcher's subscription. When non-nil, the fetcher
	// uses it to filter non-running VMs. When nil (non-wildcard
	// resource groups), power-state filtering is skipped.
	vmPowerStates *vmPowerStates
}

func newAzureInstanceFetcher(cfg azureFetcherConfig) *azureInstanceFetcher {
	return &azureInstanceFetcher{
		InstallerParams:     cfg.Matcher.Params,
		AzureClientGetter:   cfg.AzureClientGetter,
		Regions:             cfg.Matcher.Regions,
		Subscription:        cfg.Subscription,
		ResourceGroup:       cfg.ResourceGroup,
		Labels:              cfg.Matcher.ResourceTags,
		DiscoveryConfigName: cfg.DiscoveryConfigName,
		Integration:         cfg.Matcher.Integration,
		Logger:              cfg.Logger,
	}
}

func (*azureInstanceFetcher) GetMatchingInstances(_ context.Context, _ []types.Server, _ bool) ([]*AzureInstances, error) {
	return nil, trace.NotImplemented("not implemented for azure fetchers")
}

func (f *azureInstanceFetcher) GetDiscoveryConfigName() string {
	return f.DiscoveryConfigName
}

// IntegrationName identifies the integration name whose credentials were used to fetch the resources.
// Might be empty when the fetcher is using ambient credentials.
func (f *azureInstanceFetcher) IntegrationName() string {
	return f.Integration
}

// GetSubscription returns the fetcher's Azure subscription.
func (f *azureInstanceFetcher) GetSubscription() string {
	return f.Subscription
}

// GetResourceGroup returns the fetcher's Azure resource group matcher.
func (f *azureInstanceFetcher) GetResourceGroup() string {
	return f.ResourceGroup
}

type resourceGroupLocation struct {
	resourceGroup string
	location      string
}

// GetInstances fetches all Azure virtual machines matching configured filters.
func (f *azureInstanceFetcher) GetInstances(ctx context.Context, _ bool) ([]*AzureInstances, error) {
	azureClients, err := f.AzureClientGetter(ctx, f.IntegrationName())
	if err != nil {
		return nil, trace.Wrap(err)
	}

	client, err := azureClients.GetVirtualMachinesClient(ctx, f.Subscription)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	vms, err := client.ListVirtualMachines(ctx, f.ResourceGroup)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	instByRegionAndRG := make(map[resourceGroupLocation][]*armcompute.VirtualMachine)

	allowAllLocations := slices.Contains(f.Regions, types.Wildcard)
	allowAllResourceGroups := f.ResourceGroup == types.Wildcard

	for _, vm := range vms {
		location := azure.StringVal(vm.Location)
		if !slices.Contains(f.Regions, location) && !allowAllLocations {
			continue
		}

		vmTags := make(map[string]string, len(vm.Tags))
		for key, value := range vm.Tags {
			vmTags[key] = azure.StringVal(value)
		}
		if match, _, _ := services.MatchLabels(f.Labels, vmTags); !match {
			continue
		}

		resourceGroup := f.ResourceGroup
		if allowAllResourceGroups {
			resourceMetadata, err := arm.ParseResourceID(azure.StringVal(vm.ID))
			if err != nil {
				f.Logger.WarnContext(ctx, "Skipping Teleport installation on Azure VM - failed to infer resource group from vm id",
					"subscription_id", f.Subscription,
					"vm_id", azure.StringVal(vm.Properties.VMID),
					"resource_id", azure.StringVal(vm.ID),
					"error", err,
				)
				continue
			}
			resourceGroup = resourceMetadata.ResourceGroupName
		}

		batchGroup := resourceGroupLocation{
			resourceGroup: resourceGroup,
			location:      location,
		}

		instByRegionAndRG[batchGroup] = append(instByRegionAndRG[batchGroup], vm)
	}

	candidateCount := 0
	for _, grouped := range instByRegionAndRG {
		candidateCount += len(grouped)
	}

	// Fetch power states to filter non-running VMs.
	// Wildcard fetchers have a vmPowerStates that calls
	// ListVirtualMachineStatuses(*, Wildcard) with StatusOnly=true.
	// Non-wildcard fetchers have nil vmPowerStates and skip
	// filtering — per-VM Get calls would create O(N) ARM
	// amplification each poll cycle.
	var powerStates map[string]azure.PowerState
	powerFilterReason := ""
	if f.vmPowerStates == nil {
		// Non-wildcard resource groups have nil vmPowerStates.
		// Power-state filtering is skipped to avoid per-VM ARM Get
		// amplification.
		powerFilterReason = "non_wildcard_resource_group"
	} else {
		powerStates, err = f.vmPowerStates.get(ctx, client)
		if err != nil {
			f.Logger.WarnContext(ctx,
				"Failed to fetch VM power states, skipping power state filter",
				"error", err,
			)
			powerStates = nil
			powerFilterReason = "status_fetch_error"
		}
	}

	if powerStates == nil {
		f.Logger.DebugContext(ctx,
			"Azure VM power-state filter not applied",
			"subscription_id", f.Subscription,
			"resource_group", f.ResourceGroup,
			"integration", f.Integration,
			"candidate_vms", candidateCount,
			"reason", powerFilterReason,
		)
	}

	// Filter non-running VMs from each batch group.
	// powerStates == nil means skip filtering (non-wildcard RG or
	// bulk call failure).
	fallbackLookups := 0
	fallbackFailures := 0
	fallbackLookupsSkipped := 0
	filteredNonRunning := 0
	for batchGroup, vms := range instByRegionAndRG {
		if powerStates == nil {
			continue
		}

		var running []*armcompute.VirtualMachine
		for _, vm := range vms {
			resourceID := azure.StringVal(vm.ID)
			vmName := azure.StringVal(vm.Name)
			rg := batchGroup.resourceGroup

			state, inMap := powerStates[resourceID]
			if !inMap {
				if fallbackLookups >= maxPowerStateFallbackLookupsPerFetch {
					fallbackLookupsSkipped++
					running = append(running, vm)
					continue
				}
				fallbackLookups++
				// VM missing from bulk response — targeted
				// per-VM fallback before deciding.
				result, getErr := client.GetVMPowerState(
					ctx, rg, vmName)
				if getErr != nil {
					fallbackFailures++
					// Fail-open: allow VM through to avoid
					// silently dropping reachable VMs.
					f.Logger.WarnContext(ctx,
						"VM missing from bulk power state response and per-VM lookup failed, allowing VM to proceed",
						"vm_name", vmName,
						"resource_id", resourceID,
						"error", getErr,
					)
					running = append(running, vm)
					continue
				}
				state = result.State
			}

			if state != azure.PowerStateRunning {
				filteredNonRunning++
				f.Logger.InfoContext(ctx,
					"Skipping Azure VM that is not running",
					"vm_name", vmName,
					"resource_id", resourceID,
					"power_state", string(state),
				)
				continue
			}

			running = append(running, vm)
		}
		instByRegionAndRG[batchGroup] = running
	}

	if powerStates != nil {
		if fallbackLookups > 0 {
			f.Logger.WarnContext(ctx,
				"Azure VMs missing from bulk power-state response, used per-VM fallback lookups",
				"subscription_id", f.Subscription,
				"resource_group", f.ResourceGroup,
				"integration", f.Integration,
				"candidate_vms", candidateCount,
				"bulk_status_entries", len(powerStates),
				"fallback_lookups", fallbackLookups,
				"fallback_failures", fallbackFailures,
			)
		}
		if fallbackLookupsSkipped > 0 {
			f.Logger.WarnContext(ctx,
				"Azure VM power-state fallback lookup limit reached, allowing remaining VMs to proceed without per-VM power check",
				"subscription_id", f.Subscription,
				"resource_group", f.ResourceGroup,
				"integration", f.Integration,
				"fallback_lookup_limit", maxPowerStateFallbackLookupsPerFetch,
				"fallback_lookups", fallbackLookups,
				"fallback_lookups_skipped", fallbackLookupsSkipped,
			)
		}

		f.Logger.DebugContext(ctx,
			"Azure VM power-state filtering summary",
			"subscription_id", f.Subscription,
			"resource_group", f.ResourceGroup,
			"integration", f.Integration,
			"candidate_vms", candidateCount,
			"bulk_status_entries", len(powerStates),
			"fallback_lookups", fallbackLookups,
			"fallback_failures", fallbackFailures,
			"fallback_lookups_skipped", fallbackLookupsSkipped,
			"filtered_non_running", filteredNonRunning,
		)
	}

	var instances []*AzureInstances
	for batchGroup, vms := range instByRegionAndRG {
		instances = append(instances, &AzureInstances{
			SubscriptionID:      f.Subscription,
			Region:              batchGroup.location,
			ResourceGroup:       batchGroup.resourceGroup,
			Instances:           vms,
			Integration:         f.Integration,
			InstallerParams:     f.InstallerParams,
			DiscoveryConfigName: f.DiscoveryConfigName,
		})
	}

	return instances, nil
}
