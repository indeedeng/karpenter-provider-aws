/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package instance

// iceOfferingKeys decides what core is told about a launch failure, and the interesting cases
// are the ones it must stay silent about. Driving reserved and placement-group launches through
// InstanceProvider.Create would need a reservation or a placement group in the fake, so these
// exercise the helper directly.

import (
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	corecloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

func fleetError(errorCode, instanceType, zone string) ec2types.CreateFleetError {
	return ec2types.CreateFleetError{
		ErrorCode:    lo.ToPtr(errorCode),
		ErrorMessage: lo.ToPtr("there is no capacity"),
		LaunchTemplateAndOverrides: &ec2types.LaunchTemplateAndOverridesResponse{
			Overrides: &ec2types.FleetLaunchTemplateOverrides{
				InstanceType:     ec2types.InstanceType(instanceType),
				AvailabilityZone: lo.ToPtr(zone),
			},
		},
	}
}

var _ = Describe("ICE offering attribution", func() {
	It("should report the instance type and zone of each unfulfillable pool", func() {
		keys := iceOfferingKeys([]ec2types.CreateFleetError{
			fleetError("InsufficientInstanceCapacity", "m5.xlarge", "test-zone-1a"),
			fleetError("InsufficientInstanceCapacity", "m5.large", "test-zone-1b"),
		}, karpv1.CapacityTypeOnDemand, false)

		Expect(keys).To(ConsistOf(
			corecloudprovider.OfferingKey{InstanceType: "m5.xlarge", CapacityType: karpv1.CapacityTypeOnDemand, Zone: "test-zone-1a"},
			corecloudprovider.OfferingKey{InstanceType: "m5.large", CapacityType: karpv1.CapacityTypeOnDemand, Zone: "test-zone-1b"},
		))
	})
	It("should carry the capacity type the launch actually used", func() {
		keys := iceOfferingKeys([]ec2types.CreateFleetError{
			fleetError("InsufficientInstanceCapacity", "m5.xlarge", "test-zone-1a"),
		}, karpv1.CapacityTypeSpot, false)

		// Spot exhaustion in a zone says nothing about on-demand in that zone, so the capacity
		// type has to be part of what core backs off.
		Expect(keys).To(ConsistOf(
			corecloudprovider.OfferingKey{InstanceType: "m5.xlarge", CapacityType: karpv1.CapacityTypeSpot, Zone: "test-zone-1a"},
		))
	})
	It("should report every unfulfillable error code, not just insufficient capacity", func() {
		keys := iceOfferingKeys([]ec2types.CreateFleetError{
			fleetError("MaxSpotInstanceCountExceeded", "m5.xlarge", "test-zone-1a"),
			fleetError("VcpuLimitExceeded", "m5.large", "test-zone-1a"),
		}, karpv1.CapacityTypeSpot, false)

		Expect(keys).To(HaveLen(2))
	})
	It("should collapse repeated errors for one pool", func() {
		keys := iceOfferingKeys([]ec2types.CreateFleetError{
			fleetError("InsufficientInstanceCapacity", "m5.xlarge", "test-zone-1a"),
			fleetError("InsufficientInstanceCapacity", "m5.xlarge", "test-zone-1a"),
			fleetError("MaxSpotInstanceCountExceeded", "m5.xlarge", "test-zone-1a"),
		}, karpv1.CapacityTypeSpot, false)

		Expect(keys).To(HaveLen(1))
	})
	It("should ignore errors that are not capacity failures", func() {
		keys := iceOfferingKeys([]ec2types.CreateFleetError{
			fleetError("AuthFailure.ServiceLinkedRoleCreationNotPermitted", "m5.xlarge", "test-zone-1a"),
			fleetError("InsufficientInstanceCapacity", "m5.large", "test-zone-1b"),
		}, karpv1.CapacityTypeSpot, false)

		// A missing service-linked role is not a property of the m5.xlarge/1a pool, and backing
		// that pool off would outlast the misconfiguration it actually indicates.
		Expect(keys).To(ConsistOf(
			corecloudprovider.OfferingKey{InstanceType: "m5.large", CapacityType: karpv1.CapacityTypeSpot, Zone: "test-zone-1b"},
		))
	})
	It("should stay silent for a reserved launch", func() {
		keys := iceOfferingKeys([]ec2types.CreateFleetError{
			fleetError("ReservationCapacityExceeded", "m5.xlarge", "test-zone-1a"),
		}, karpv1.CapacityTypeReserved, false)

		// An exhausted reservation is narrower than the instanceType/zone pool core would back
		// off. The capacity reservation provider already tracks it by reservation ID.
		Expect(keys).To(BeEmpty())
	})
	It("should stay silent for a placement group scoped launch", func() {
		keys := iceOfferingKeys([]ec2types.CreateFleetError{
			fleetError("InsufficientInstanceCapacity", "m5.xlarge", "test-zone-1a"),
		}, karpv1.CapacityTypeOnDemand, true)

		// The pool may have capacity for anyone not asking to land inside this placement group,
		// and core's backoff is global across NodePools, so reporting it would block launches
		// that would have succeeded.
		Expect(keys).To(BeEmpty())
	})
	It("should skip errors with no launch attribution", func() {
		keys := iceOfferingKeys([]ec2types.CreateFleetError{
			{ErrorCode: lo.ToPtr("InsufficientInstanceCapacity"), ErrorMessage: lo.ToPtr("no capacity")},
			{
				ErrorCode:                  lo.ToPtr("InsufficientInstanceCapacity"),
				LaunchTemplateAndOverrides: &ec2types.LaunchTemplateAndOverridesResponse{},
			},
			fleetError("InsufficientInstanceCapacity", "", "test-zone-1a"),
			fleetError("InsufficientInstanceCapacity", "m5.xlarge", ""),
			fleetError("InsufficientInstanceCapacity", "m5.large", "test-zone-1b"),
		}, karpv1.CapacityTypeOnDemand, false)

		Expect(keys).To(ConsistOf(
			corecloudprovider.OfferingKey{InstanceType: "m5.large", CapacityType: karpv1.CapacityTypeOnDemand, Zone: "test-zone-1b"},
		))
	})
	It("should report nothing when there are no errors", func() {
		Expect(iceOfferingKeys(nil, karpv1.CapacityTypeOnDemand, false)).To(BeEmpty())
	})
})
