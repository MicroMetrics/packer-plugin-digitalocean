//go:generate packer-sdc struct-markdown
//go:generate packer-sdc mapstructure-to-hcl2 -type Config

package digitalocean

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/digitalocean/godo"
	"github.com/hashicorp/packer-plugin-sdk/common"
	"github.com/hashicorp/packer-plugin-sdk/communicator"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
	"github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/hashicorp/packer-plugin-sdk/template/interpolate"
	"github.com/hashicorp/packer-plugin-sdk/uuid"
	"github.com/mitchellh/mapstructure"
)

type Config struct {
	common.PackerConfig `mapstructure:",squash"`
	Comm                communicator.Config `mapstructure:",squash"`
	// The client TOKEN to use to access your account. It
	// can also be specified via environment variable DIGITALOCEAN_TOKEN, DIGITALOCEAN_ACCESS_TOKEN, or DIGITALOCEAN_API_TOKEN if
	// set. DIGITALOCEAN_API_TOKEN will be deprecated in a future release in favor of DIGITALOCEAN_TOKEN or DIGITALOCEAN_ACCESS_TOKEN.
	APIToken string `mapstructure:"api_token" required:"true"`
	// Non standard api endpoint URL. Set this if you are
	// using a DigitalOcean API compatible service. It can also be specified via
	// environment variable DIGITALOCEAN_API_URL.
	APIURL string `mapstructure:"api_url" required:"false"`
	// The maximum number of retries for requests that fail with a 429 or 500-level error.
	// The default value is 5. Set to 0 to disable reties.
	HTTPRetryMax *int `mapstructure:"http_retry_max" required:"false"`
	// The maximum wait time (in seconds) between failed API requests. Default: 30.0
	HTTPRetryWaitMax *float64 `mapstructure:"http_retry_wait_max" required:"false"`
	// The minimum wait time (in seconds) between failed API requests. Default: 1.0
	HTTPRetryWaitMin *float64 `mapstructure:"http_retry_wait_min" required:"false"`
	// The name (or slug) of the region to launch the droplet
	// in. Consequently, this is the region where the snapshot will be available.
	// See
	// https://docs.digitalocean.com/reference/api/api-reference/#operation/list_all_regions
	// for the accepted region names/slugs.
	Region string `mapstructure:"region" required:"true"`
	// The name (or slug) of the droplet size to use. See
	// https://docs.digitalocean.com/reference/api/api-reference/#operation/list_all_sizes
	// for the accepted size names/slugs.
	Size string `mapstructure:"size" required:"true"`
	// The name (or slug) of the base image to use. This is the
	// image that will be used to launch a new droplet and provision it. See
	// https://docs.digitalocean.com/reference/api/api-reference/#operation/get_images_list
	// for details on how to get a list of the accepted image names/slugs.
	Image string `mapstructure:"image" required:"true"`
	// Set to true to enable private networking
	// for the droplet being created. This defaults to false, or not enabled.
	PrivateNetworking bool `mapstructure:"private_networking" required:"false"`
	// Set to true to enable monitoring for the droplet
	// being created. This defaults to false, or not enabled.
	Monitoring bool `mapstructure:"monitoring" required:"false"`
	// A boolean indicating whether to install the DigitalOcean agent used for
	// providing access to the Droplet web console in the control panel. By
	// default, the agent is installed on new Droplets but installation errors
	// (i.e. OS not supported) are ignored. To prevent it from being installed,
	// set to false. To make installation errors fatal, explicitly set it to true.
	DropletAgent *bool `mapstructure:"droplet_agent" required:"false"`
	// Set to true to enable ipv6 for the droplet being
	// created. This defaults to false, or not enabled.
	IPv6 bool `mapstructure:"ipv6" required:"false"`
	// The name of the resulting snapshot that will
	// appear in your account. Defaults to `packer-{{timestamp}}` (see
	// configuration templates for more info).
	SnapshotName string `mapstructure:"snapshot_name" required:"false"`
	// Additional regions that resulting snapshot should be distributed to.
	SnapshotRegions []string `mapstructure:"snapshot_regions" required:"false"`
	// When true, Packer will block until all snapshot transfers have been completed
	// and report errors. When false, Packer will initiate the snapshot transfers
	// and exit successfully without waiting for completion. Defaults to true.
	WaitSnapshotTransfer *bool `mapstructure:"wait_snapshot_transfer" required:"false"`
	// How long to wait for a snapshot to be transferred to an additional region
	// before timing out. The default transfer timeout is "30m" (valid time units
	// include `s` for seconds, `m` for minutes, and `h` for hours).
	TransferTimeout time.Duration `mapstructure:"transfer_timeout" required:"false"`
	// The time to wait, as a duration string, for a
	// droplet to enter a desired state (such as "active") before timing out. The
	// default state timeout is "6m".
	StateTimeout time.Duration `mapstructure:"state_timeout" required:"false"`
	// How long to wait for the Droplet snapshot to complete before timing out.
	// The default snapshot timeout is "60m" (valid time units include `s` for
	// seconds, `m` for minutes, and `h` for hours).
	SnapshotTimeout time.Duration `mapstructure:"snapshot_timeout" required:"false"`
	// The name assigned to the droplet. DigitalOcean
	// sets the hostname of the machine to this value.
	DropletName string `mapstructure:"droplet_name" required:"false"`
	// User data to launch with the Droplet. Packer will
	// not automatically wait for a user script to finish before shutting down the
	// instance this must be handled in a provisioner.
	UserData string `mapstructure:"user_data" required:"false"`
	// Path to a file that will be used for the user
	// data when launching the Droplet.
	UserDataFile string `mapstructure:"user_data_file" required:"false"`
	// Tags to apply to the droplet when it is created
	Tags []string `mapstructure:"tags" required:"false"`
	// UUID of the VPC which the droplet will be created in. Before using this,
	// private_networking should be enabled.
	VPCUUID string `mapstructure:"vpc_uuid" required:"false"`
	// Wheter the communicators should use private IP or not (public IP in that case).
	// If the droplet is or going to be accessible only from the local network because
	// it is at behind a firewall, then communicators should use the private IP
	// instead of the public IP. Before using this, private_networking should be enabled.
	ConnectWithPrivateIP bool `mapstructure:"connect_with_private_ip" required:"false"`
	// The ID of an existing SSH key on the DigitalOcean account. This should be
	// used in conjunction with `ssh_private_key_file`.
	SSHKeyID int `mapstructure:"ssh_key_id" required:"false"`
	// Set to true if you are connecting as a non-root user whose public key is
	// already available on the base image.
	SkipKeygen bool `mapstructure:"skip_keygen" required:"false"`

	ctx interpolate.Context
}

func (c *Config) Prepare(raws ...interface{}) ([]string, error) {

	// Accumulate warnings and errors
	var errs *packersdk.MultiError
	var warns []string

	var md mapstructure.Metadata
	err := config.Decode(c, &config.DecodeOpts{
		Metadata:           &md,
		Interpolate:        true,
		InterpolateContext: &c.ctx,
		InterpolateFilter: &interpolate.RenderFilter{
			Exclude: []string{
				"run_command",
			},
		},
	}, raws...)
	if err != nil {
		return nil, err
	}

	// Defaults
	if c.APIToken == "" {
		// Default to environment variable for api_token, if it exists
		c.APIToken = os.Getenv("DIGITALOCEAN_TOKEN")
		if c.APIToken == "" {
			c.APIToken = os.Getenv("DIGITALOCEAN_ACCESS_TOKEN")
		}
		if c.APIToken == "" {
			c.APIToken = os.Getenv("DIGITALOCEAN_API_TOKEN")
			if c.APIToken != "" {
				warns = append(warns, "The DIGITALOCEAN_API_TOKEN environment variable is deprecated "+
					"and will produce an error in future versions of the DigitalOcean Packer plugin. "+
					"Please use either DIGITALOCEAN_TOKEN or DIGITALOCEAN_ACCESS_TOKEN moving forward.")
			}
		}
	}
	if c.APIURL == "" {
		c.APIURL = os.Getenv("DIGITALOCEAN_API_URL")
	}
	if c.HTTPRetryMax == nil {
		c.HTTPRetryMax = godo.PtrTo(5)
		if max := os.Getenv("DIGITALOCEAN_HTTP_RETRY_MAX"); max != "" {
			maxInt, err := strconv.Atoi(max)
			if err != nil {
				return nil, err
			}
			c.HTTPRetryMax = godo.PtrTo(maxInt)
		}
	}
	if c.HTTPRetryWaitMax == nil {
		c.HTTPRetryWaitMax = godo.PtrTo(30.0)
		if waitMax := os.Getenv("DIGITALOCEAN_HTTP_RETRY_WAIT_MAX"); waitMax != "" {
			waitMaxFloat, err := strconv.ParseFloat(waitMax, 64)
			if err != nil {
				return nil, err
			}
			c.HTTPRetryWaitMax = godo.PtrTo(waitMaxFloat)
		}
	}
	if c.HTTPRetryWaitMin == nil {
		c.HTTPRetryWaitMin = godo.PtrTo(1.0)
		if waitMin := os.Getenv("DIGITALOCEAN_HTTP_RETRY_WAIT_MIN"); waitMin != "" {
			waitMinFloat, err := strconv.ParseFloat(waitMin, 64)
			if err != nil {
				return nil, err
			}
			c.HTTPRetryWaitMin = godo.PtrTo(waitMinFloat)
		}
	}

	if c.SnapshotName == "" {
		def, err := interpolate.Render("packer-{{timestamp}}", nil)
		if err != nil {
			panic(err)
		}

		// Default to packer-{{ unix timestamp (utc) }}
		c.SnapshotName = def
	}

	if c.DropletName == "" {
		// Default to packer-[time-ordered-uuid]
		c.DropletName = fmt.Sprintf("packer-%s", uuid.TimeOrderedUUID())
	}

	if c.StateTimeout == 0 {
		// Default to 6 minute timeouts waiting for
		// desired state. i.e waiting for droplet to become active
		c.StateTimeout = 6 * time.Minute
	}

	if c.SnapshotTimeout == 0 {
		// Default to 60 minutes timeout, waiting for snapshot action to finish
		c.SnapshotTimeout = 60 * time.Minute
	}

	if c.TransferTimeout == 0 {
		c.TransferTimeout = 30 * time.Minute
	}

	if c.WaitSnapshotTransfer == nil {
		c.WaitSnapshotTransfer = godo.PtrTo(true)
	}

	if es := c.Comm.Prepare(&c.ctx); len(es) > 0 {
		errs = packersdk.MultiErrorAppend(errs, es...)
	}
	if c.APIToken == "" {
		// Required configurations that will display errors if not set
		errs = packersdk.MultiErrorAppend(
			errs, errors.New("api_token for auth must be specified"))
	}

	if c.Region == "" {
		errs = packersdk.MultiErrorAppend(
			errs, errors.New("region is required"))
	}

	if c.Size == "" {
		errs = packersdk.MultiErrorAppend(
			errs, errors.New("size is required"))
	}

	if c.Image == "" {
		errs = packersdk.MultiErrorAppend(
			errs, errors.New("image is required"))
	}

	if c.UserData != "" && c.UserDataFile != "" {
		errs = packersdk.MultiErrorAppend(
			errs, errors.New("only one of user_data or user_data_file can be specified"))
	} else if c.UserDataFile != "" {
		if _, err := os.Stat(c.UserDataFile); err != nil {
			errs = packersdk.MultiErrorAppend(
				errs, fmt.Errorf("user_data_file not found: %s", c.UserDataFile))
		}
	}

	if c.Tags == nil {
		c.Tags = make([]string, 0)
	}
	tagRe := regexp.MustCompile("^[[:alnum:]:_-]{1,255}$")

	for _, t := range c.Tags {
		if !tagRe.MatchString(t) {
			errs = packersdk.MultiErrorAppend(errs, fmt.Errorf("invalid tag: %s", t))
		}
	}

	// Check if the PrivateNetworking is enabled by user before use VPC UUID
	if c.VPCUUID != "" {
		if !c.PrivateNetworking {
			errs = packersdk.MultiErrorAppend(errs, errors.New("private networking should be enabled to use vpc_uuid"))
		}
	}

	// Check if the PrivateNetworking is enabled by user before use ConnectWithPrivateIP
	if c.ConnectWithPrivateIP {
		if !c.PrivateNetworking {
			errs = packersdk.MultiErrorAppend(errs, errors.New("private networking should be enabled to use connect_with_private_ip"))
		}
	}

	if errs != nil && len(errs.Errors) > 0 {
		return warns, errs
	}

	packersdk.LogSecretFilter.Set(c.APIToken)
	return warns, nil
}
