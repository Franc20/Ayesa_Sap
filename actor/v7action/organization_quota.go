package v7action

import (
	"code.cloudfoundry.org/cli/actor/actionerror"
	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv3"
	"code.cloudfoundry.org/cli/types"
)

type OrganizationQuota struct {
	// GUID is the unique ID of the organization quota.
	GUID string
	// Name is the name of the organization quota
	Name string

	//the various limits that are associated with applications
	TotalMemory       types.NullInt
	InstanceMemory    types.NullInt
	TotalAppInstances types.NullInt

	//the various limits that are associated with services
	TotalServiceInstances types.NullInt
	PaidServicePlans      bool

	//the various limits that are associated with routes
	TotalRoutes     types.NullInt
	TotalRoutePorts types.NullInt
}

func (actor Actor) GetOrganizationQuotas() ([]OrganizationQuota, Warnings, error) {
	ccv3OrgQuotas, warnings, err := actor.CloudControllerClient.GetOrganizationQuotas()
	if err != nil {
		return []OrganizationQuota{}, Warnings(warnings), err
	}

	var orgQuotas []OrganizationQuota
	for _, quota := range ccv3OrgQuotas {
		orgQuotas = append(orgQuotas, convertToOrganizationQuota(quota))
	}

	return orgQuotas, Warnings(warnings), nil
}

func (actor Actor) GetOrganizationQuotaByName(orgQuotaName string) (OrganizationQuota, Warnings, error) {
	ccv3OrgQuotas, warnings, err := actor.CloudControllerClient.GetOrganizationQuotas(
		ccv3.Query{
			Key:    ccv3.NameFilter,
			Values: []string{orgQuotaName},
		},
	)
	if err != nil {
		return OrganizationQuota{}, Warnings(warnings), err

	}

	if len(ccv3OrgQuotas) == 0 {
		return OrganizationQuota{}, Warnings(warnings), actionerror.OrganizationQuotaNotFoundForNameError{Name: orgQuotaName}
	}
	orgQuota := convertToOrganizationQuota(ccv3OrgQuotas[0])

	return orgQuota, Warnings(warnings), nil
}

func convertToOrganizationQuota(ccv3OrgQuota ccv3.OrgQuota) OrganizationQuota {
	orgQuota := OrganizationQuota{
		GUID:                  ccv3OrgQuota.GUID,
		Name:                  ccv3OrgQuota.Name,
		TotalMemory:           ccv3OrgQuota.Apps.TotalMemory,
		InstanceMemory:        ccv3OrgQuota.Apps.InstanceMemory,
		TotalAppInstances:     ccv3OrgQuota.Apps.TotalAppInstances,
		TotalServiceInstances: ccv3OrgQuota.Services.TotalServiceInstances,
		PaidServicePlans:      ccv3OrgQuota.Services.PaidServicePlans,
		TotalRoutes:           ccv3OrgQuota.Routes.TotalRoutes,
		TotalRoutePorts:       ccv3OrgQuota.Routes.TotalRoutePorts,
	}
	return orgQuota
}
