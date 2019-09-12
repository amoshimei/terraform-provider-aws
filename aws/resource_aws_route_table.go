package aws

import (
	"bytes"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/hashicorp/terraform/helper/hashcode"
	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/helper/schema"
)

func resourceAwsRouteTable() *schema.Resource {
	return &schema.Resource{
		Create: resourceAwsRouteTableCreate,
		Read:   resourceAwsRouteTableRead,
		Update: resourceAwsRouteTableUpdate,
		Delete: resourceAwsRouteTableDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Schema: map[string]*schema.Schema{
			"vpc_id": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"tags": tagsSchema(),

			"propagating_vgws": {
				Type:     schema.TypeSet,
				Optional: true,
				Computed: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Set:      schema.HashString,
			},

			"route": {
				Type:       schema.TypeSet,
				Computed:   true,
				Optional:   true,
				ConfigMode: schema.SchemaConfigModeAttr,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"cidr_block": {
							Type:     schema.TypeString,
							Optional: true,
						},

						"ipv6_cidr_block": {
							Type:     schema.TypeString,
							Optional: true,
						},

						"egress_only_gateway_id": {
							Type:     schema.TypeString,
							Optional: true,
						},

						"gateway_id": {
							Type:     schema.TypeString,
							Optional: true,
						},

						"instance_id": {
							Type:     schema.TypeString,
							Optional: true,
						},

						"nat_gateway_id": {
							Type:     schema.TypeString,
							Optional: true,
						},

						"transit_gateway_id": {
							Type:     schema.TypeString,
							Optional: true,
						},

						"vpc_peering_connection_id": {
							Type:     schema.TypeString,
							Optional: true,
						},

						"network_interface_id": {
							Type:     schema.TypeString,
							Optional: true,
						},
					},
				},
				Set: resourceAwsRouteTableHash,
			},

			"owner_id": {
				Type:     schema.TypeString,
				Computed: true,
			},
		},
	}
}

func resourceAwsRouteTableCreate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).ec2conn

	// Create the routing table
	createOpts := &ec2.CreateRouteTableInput{
		VpcId: aws.String(d.Get("vpc_id").(string)),
	}
	log.Printf("[DEBUG] RouteTable create config: %#v", createOpts)

	resp, err := conn.CreateRouteTable(createOpts)
	if err != nil {
		return fmt.Errorf("Error creating route table: %s", err)
	}

	// Get the ID and store it
	rt := resp.RouteTable
	d.SetId(*rt.RouteTableId)
	log.Printf("[INFO] Route Table ID: %s", d.Id())

	// Wait for the route table to become available
	log.Printf(
		"[DEBUG] Waiting for route table (%s) to become available",
		d.Id())
	stateConf := &resource.StateChangeConf{
		Pending:        []string{"pending"},
		Target:         []string{"ready"},
		Refresh:        resourceAwsRouteTableStateRefreshFunc(conn, d.Id()),
		Timeout:        10 * time.Minute,
		NotFoundChecks: 40,
	}
	if _, err := stateConf.WaitForState(); err != nil {
		return fmt.Errorf(
			"Error waiting for route table (%s) to become available: %s",
			d.Id(), err)
	}

	return resourceAwsRouteTableUpdate(d, meta)
}

func resourceAwsRouteTableRead(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).ec2conn

	rtRaw, _, err := resourceAwsRouteTableStateRefreshFunc(conn, d.Id())()
	if err != nil {
		return err
	}
	if rtRaw == nil {
		d.SetId("")
		return nil
	}

	rt := rtRaw.(*ec2.RouteTable)
	d.Set("vpc_id", rt.VpcId)

	propagatingVGWs := make([]string, 0, len(rt.PropagatingVgws))
	for _, vgw := range rt.PropagatingVgws {
		propagatingVGWs = append(propagatingVGWs, *vgw.GatewayId)
	}
	d.Set("propagating_vgws", propagatingVGWs)

	// Create an empty schema.Set to hold all routes
	route := &schema.Set{F: resourceAwsRouteTableHash}

	// Loop through the routes and add them to the set
	for _, r := range rt.Routes {
		if r.GatewayId != nil && *r.GatewayId == "local" {
			continue
		}

		if r.Origin != nil && *r.Origin == "EnableVgwRoutePropagation" {
			continue
		}

		if r.DestinationPrefixListId != nil {
			// Skipping because VPC endpoint routes are handled separately
			// See aws_vpc_endpoint
			continue
		}

		m := make(map[string]interface{})

		if r.DestinationCidrBlock != nil {
			m["cidr_block"] = *r.DestinationCidrBlock
		}
		if r.DestinationIpv6CidrBlock != nil {
			m["ipv6_cidr_block"] = *r.DestinationIpv6CidrBlock
		}
		if r.EgressOnlyInternetGatewayId != nil {
			m["egress_only_gateway_id"] = *r.EgressOnlyInternetGatewayId
		}
		if r.GatewayId != nil {
			m["gateway_id"] = *r.GatewayId
		}
		if r.NatGatewayId != nil {
			m["nat_gateway_id"] = *r.NatGatewayId
		}
		if r.InstanceId != nil {
			m["instance_id"] = *r.InstanceId
		}
		if r.TransitGatewayId != nil {
			m["transit_gateway_id"] = *r.TransitGatewayId
		}
		if r.VpcPeeringConnectionId != nil {
			m["vpc_peering_connection_id"] = *r.VpcPeeringConnectionId
		}
		if r.NetworkInterfaceId != nil {
			m["network_interface_id"] = *r.NetworkInterfaceId
		}

		route.Add(m)
	}
	d.Set("route", route)

	// Tags
	_, tagsAreDefined := d.GetOk("tags")
	if tagsAreDefined {
		d.Set("tags", tagsToMap(rt.Tags))
	}

	d.Set("owner_id", rt.OwnerId)

	return nil
}

func resourceAwsRouteTableUpdate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).ec2conn

	if d.HasChange("propagating_vgws") {
		o, n := d.GetChange("propagating_vgws")
		os := o.(*schema.Set)
		ns := n.(*schema.Set)
		remove := os.Difference(ns).List()
		add := ns.Difference(os).List()

		// Now first loop through all the old propagations and disable any obsolete ones
		for _, vgw := range remove {
			id := vgw.(string)

			// Disable the propagation as it no longer exists in the config
			log.Printf(
				"[INFO] Deleting VGW propagation from %s: %s",
				d.Id(), id)
			_, err := conn.DisableVgwRoutePropagation(&ec2.DisableVgwRoutePropagationInput{
				RouteTableId: aws.String(d.Id()),
				GatewayId:    aws.String(id),
			})
			if err != nil {
				return err
			}
		}

		// Make sure we save the state of the currently configured rules
		propagatingVGWs := os.Intersection(ns)
		d.Set("propagating_vgws", propagatingVGWs)

		// Then loop through all the newly configured propagations and enable them
		for _, vgw := range add {
			id := vgw.(string)

			var err error
			for i := 0; i < 5; i++ {
				log.Printf("[INFO] Enabling VGW propagation for %s: %s", d.Id(), id)
				_, err = conn.EnableVgwRoutePropagation(&ec2.EnableVgwRoutePropagationInput{
					RouteTableId: aws.String(d.Id()),
					GatewayId:    aws.String(id),
				})
				if err == nil {
					break
				}

				// If we get a Gateway.NotAttached, it is usually some
				// eventually consistency stuff. So we have to just wait a
				// bit...
				ec2err, ok := err.(awserr.Error)
				if ok && ec2err.Code() == "Gateway.NotAttached" {
					time.Sleep(20 * time.Second)
					continue
				}
			}
			if err != nil {
				return err
			}

			propagatingVGWs.Add(vgw)
			d.Set("propagating_vgws", propagatingVGWs)
		}
	}

	// Check if the route set as a whole has changed
	if d.HasChange("route") {
		o, n := d.GetChange("route")
		ors := o.(*schema.Set).Difference(n.(*schema.Set))
		nrs := n.(*schema.Set).Difference(o.(*schema.Set))

		// Now first loop through all the old routes and delete any obsolete ones
		for _, route := range ors.List() {
			m := route.(map[string]interface{})

			deleteOpts := &ec2.DeleteRouteInput{
				RouteTableId: aws.String(d.Id()),
			}

			if s := m["ipv6_cidr_block"].(string); s != "" {
				deleteOpts.DestinationIpv6CidrBlock = aws.String(s)

				log.Printf(
					"[INFO] Deleting route from %s: %s",
					d.Id(), m["ipv6_cidr_block"].(string))
			}

			if s := m["cidr_block"].(string); s != "" {
				deleteOpts.DestinationCidrBlock = aws.String(s)

				log.Printf(
					"[INFO] Deleting route from %s: %s",
					d.Id(), m["cidr_block"].(string))
			}

			_, err := conn.DeleteRoute(deleteOpts)
			if err != nil {
				return err
			}
		}

		// Make sure we save the state of the currently configured rules
		routes := o.(*schema.Set).Intersection(n.(*schema.Set))
		d.Set("route", routes)

		// Then loop through all the newly configured routes and create them
		for _, route := range nrs.List() {
			m := route.(map[string]interface{})

			opts := ec2.CreateRouteInput{
				RouteTableId: aws.String(d.Id()),
			}

			if s := m["transit_gateway_id"].(string); s != "" {
				opts.TransitGatewayId = aws.String(s)
			}

			if s := m["vpc_peering_connection_id"].(string); s != "" {
				opts.VpcPeeringConnectionId = aws.String(s)
			}

			if s := m["network_interface_id"].(string); s != "" {
				opts.NetworkInterfaceId = aws.String(s)
			}

			if s := m["instance_id"].(string); s != "" {
				opts.InstanceId = aws.String(s)
			}

			if s := m["ipv6_cidr_block"].(string); s != "" {
				opts.DestinationIpv6CidrBlock = aws.String(s)
			}

			if s := m["cidr_block"].(string); s != "" {
				opts.DestinationCidrBlock = aws.String(s)
			}

			if s := m["gateway_id"].(string); s != "" {
				opts.GatewayId = aws.String(s)
			}

			if s := m["egress_only_gateway_id"].(string); s != "" {
				opts.EgressOnlyInternetGatewayId = aws.String(s)
			}

			if s := m["nat_gateway_id"].(string); s != "" {
				opts.NatGatewayId = aws.String(s)
			}

			log.Printf("[INFO] Creating route for %s: %#v", d.Id(), opts)
			err := resource.Retry(5*time.Minute, func() *resource.RetryError {
				_, err := conn.CreateRoute(&opts)

				if isAWSErr(err, "InvalidRouteTableID.NotFound", "") {
					return resource.RetryableError(err)
				}

				if isAWSErr(err, "InvalidTransitGatewayID.NotFound", "") {
					return resource.RetryableError(err)
				}

				if err != nil {
					return resource.NonRetryableError(err)
				}
				return nil
			})
			if isResourceTimeoutError(err) {
				_, err = conn.CreateRoute(&opts)
			}
			if err != nil {
				return fmt.Errorf("Error creating route: %s", err)
			}

			routes.Add(route)
			d.Set("route", routes)
		}
	}

	if d.HasChange("tags") {
		if err := setTags(conn, d); err != nil {
			return err
		} else {
			d.SetPartial("tags")
		}
	}

	return resourceAwsRouteTableRead(d, meta)
}

func resourceAwsRouteTableDelete(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).ec2conn

	// First request the routing table since we'll have to disassociate
	// all the subnets first.
	rtRaw, _, err := resourceAwsRouteTableStateRefreshFunc(conn, d.Id())()
	if err != nil {
		return err
	}
	if rtRaw == nil {
		return nil
	}
	rt := rtRaw.(*ec2.RouteTable)

	// Do all the disassociations
	for _, a := range rt.Associations {
		log.Printf("[INFO] Disassociating association: %s", *a.RouteTableAssociationId)
		_, err := conn.DisassociateRouteTable(&ec2.DisassociateRouteTableInput{
			AssociationId: a.RouteTableAssociationId,
		})
		if err != nil {
			// First check if the association ID is not found. If this
			// is the case, then it was already disassociated somehow,
			// and that is okay.
			if ec2err, ok := err.(awserr.Error); ok && ec2err.Code() == "InvalidAssociationID.NotFound" {
				err = nil
			}
		}
		if err != nil {
			return err
		}
	}

	// Delete the route table
	log.Printf("[INFO] Deleting Route Table: %s", d.Id())
	_, err = conn.DeleteRouteTable(&ec2.DeleteRouteTableInput{
		RouteTableId: aws.String(d.Id()),
	})
	if err != nil {
		ec2err, ok := err.(awserr.Error)
		if ok && ec2err.Code() == "InvalidRouteTableID.NotFound" {
			return nil
		}

		return fmt.Errorf("Error deleting route table: %s", err)
	}

	// Wait for the route table to really destroy
	log.Printf(
		"[DEBUG] Waiting for route table (%s) to become destroyed",
		d.Id())

	stateConf := &resource.StateChangeConf{
		Pending: []string{"ready"},
		Target:  []string{},
		Refresh: resourceAwsRouteTableStateRefreshFunc(conn, d.Id()),
		Timeout: 5 * time.Minute,
	}
	if _, err := stateConf.WaitForState(); err != nil {
		return fmt.Errorf(
			"Error waiting for route table (%s) to become destroyed: %s",
			d.Id(), err)
	}

	return nil
}

func resourceAwsRouteTableHash(v interface{}) int {
	var buf bytes.Buffer
	m, castOk := v.(map[string]interface{})
	if !castOk {
		return 0
	}

	if v, ok := m["ipv6_cidr_block"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}

	if v, ok := m["cidr_block"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}

	if v, ok := m["gateway_id"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}

	if v, ok := m["egress_only_gateway_id"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}

	natGatewaySet := false
	if v, ok := m["nat_gateway_id"]; ok {
		natGatewaySet = v.(string) != ""
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}

	instanceSet := false
	if v, ok := m["instance_id"]; ok {
		instanceSet = v.(string) != ""
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}

	if v, ok := m["transit_gateway_id"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}

	if v, ok := m["vpc_peering_connection_id"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}

	if v, ok := m["network_interface_id"]; ok && !(instanceSet || natGatewaySet) {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}

	return hashcode.String(buf.String())
}

// resourceAwsRouteTableStateRefreshFunc returns a resource.StateRefreshFunc that is used to watch
// a RouteTable.
func resourceAwsRouteTableStateRefreshFunc(conn *ec2.EC2, id string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		resp, err := conn.DescribeRouteTables(&ec2.DescribeRouteTablesInput{
			RouteTableIds: []*string{aws.String(id)},
		})
		if err != nil {
			if ec2err, ok := err.(awserr.Error); ok && ec2err.Code() == "InvalidRouteTableID.NotFound" {
				resp = nil
			} else {
				log.Printf("Error on RouteTableStateRefresh: %s", err)
				return nil, "", err
			}
		}

		if resp == nil {
			// Sometimes AWS just has consistency issues and doesn't see
			// our instance yet. Return an empty state.
			return nil, "", nil
		}

		rt := resp.RouteTables[0]
		return rt, "ready", nil
	}
}
