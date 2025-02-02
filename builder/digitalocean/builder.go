// The digitalocean package contains a packersdk.Builder implementation
// that builds DigitalOcean images (snapshots).

package digitalocean

import (
	"context"
	"fmt"
	"log"
	"net/url"

	"github.com/digitalocean/godo"
	"github.com/digitalocean/packer-plugin-digitalocean/version"
	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/communicator"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/multistep/commonsteps"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
	"github.com/hashicorp/packer-plugin-sdk/useragent"
	"golang.org/x/oauth2"
)

// The unique id for the builder
const BuilderId = "pearkes.digitalocean"

type Builder struct {
	config Config
	runner multistep.Runner
}

var _ packersdk.Builder = new(Builder)

func (b *Builder) ConfigSpec() hcldec.ObjectSpec { return b.config.FlatMapstructure().HCL2Spec() }

func (b *Builder) Prepare(raws ...interface{}) ([]string, []string, error) {

	warnings, errs := b.config.Prepare(raws...)
	if b.config.SSHKeyID != 0 && b.config.Comm.SSHPrivateKeyFile == "" {
		errs = packersdk.MultiErrorAppend(errs,
			fmt.Errorf("Must specify a `ssh_private_key_file` when using `ssh_key_id`."))
	}
	if errs != nil {
		return nil, warnings, errs
	}

	return nil, warnings, nil
}

func (b *Builder) Run(ctx context.Context, ui packersdk.Ui, hook packersdk.Hook) (packersdk.Artifact, error) {
	ua := useragent.String(version.PluginVersion.FormattedVersion())
	opts := []godo.ClientOpt{godo.SetUserAgent(ua)}
	if b.config.APIURL != "" {
		_, err := url.Parse(b.config.APIURL)
		if err != nil {
			return nil, fmt.Errorf("DigitalOcean: Invalid API URL, %s.", err)
		}

		opts = append(opts, godo.SetBaseURL(b.config.APIURL))
	}
	if *b.config.HTTPRetryMax > 0 {
		opts = append(opts, godo.WithRetryAndBackoffs(godo.RetryConfig{
			RetryMax:     *b.config.HTTPRetryMax,
			RetryWaitMin: b.config.HTTPRetryWaitMin,
			RetryWaitMax: b.config.HTTPRetryWaitMax,
			Logger:       log.Default(),
		}))
	}

	client, err := godo.New(oauth2.NewClient(context.TODO(), &APITokenSource{
		AccessToken: b.config.APIToken,
	}), opts...)
	if err != nil {
		return nil, fmt.Errorf("DigitalOcean: could not create client, %s", err)
	}

	if len(b.config.SnapshotRegions) > 0 {
		opt := &godo.ListOptions{
			Page:    1,
			PerPage: 200,
		}
		regions, _, err := client.Regions.List(context.TODO(), opt)
		if err != nil {
			return nil, fmt.Errorf("DigitalOcean: Unable to get regions, %s", err)
		}

		validRegions := make(map[string]struct{})
		for _, val := range regions {
			validRegions[val.Slug] = struct{}{}
		}

		for _, region := range append(b.config.SnapshotRegions, b.config.Region) {
			if _, ok := validRegions[region]; !ok {
				return nil, fmt.Errorf("DigitalOcean: Invalid region, %s", region)
			}
		}
	}

	// Set up the state
	state := new(multistep.BasicStateBag)
	state.Put("config", &b.config)
	state.Put("client", client)
	state.Put("hook", hook)
	state.Put("ui", ui)

	// Only generate the temp key pair if one is not already provided
	genTempKeyPair := !b.config.SkipKeygen && (b.config.SSHKeyID == 0 || b.config.Comm.SSHPrivateKeyFile == "")

	// Build the steps
	steps := []multistep.Step{
		multistep.If(genTempKeyPair,
			&communicator.StepSSHKeyGen{
				CommConf:            &b.config.Comm,
				SSHTemporaryKeyPair: b.config.Comm.SSH.SSHTemporaryKeyPair,
			},
		),
		multistep.If(b.config.PackerDebug && b.config.Comm.SSHPrivateKeyFile == "",
			&communicator.StepDumpSSHKey{
				Path: fmt.Sprintf("do_%s.pem", b.config.PackerBuildName),
				SSH:  &b.config.Comm.SSH,
			},
		),
		multistep.If(genTempKeyPair, new(stepCreateSSHKey)),
		new(stepCreateDroplet),
		new(stepDropletInfo),
		&communicator.StepConnect{
			Config:    &b.config.Comm,
			Host:      communicator.CommHost(b.config.Comm.Host(), "droplet_ip"),
			SSHConfig: b.config.Comm.SSHConfigFunc(),
		},
		new(commonsteps.StepProvision),
		multistep.If(genTempKeyPair,
			&commonsteps.StepCleanupTempKeys{
				Comm: &b.config.Comm,
			},
		),
		new(stepShutdown),
		new(stepPowerOff),
		&stepSnapshot{
			snapshotTimeout:         b.config.SnapshotTimeout,
			transferTimeout:         b.config.TransferTimeout,
			waitForSnapshotTransfer: *b.config.WaitSnapshotTransfer,
		},
	}

	// Run the steps
	b.runner = commonsteps.NewRunner(steps, b.config.PackerConfig, ui)
	b.runner.Run(ctx, state)

	// If there was an error, return that
	if rawErr, ok := state.GetOk("error"); ok {
		return nil, rawErr.(error)
	}

	if _, ok := state.GetOk("snapshot_name"); !ok {
		log.Println("Failed to find snapshot_name in state. Bug?")
		return nil, nil
	}

	artifact := &Artifact{
		SnapshotName: state.Get("snapshot_name").(string),
		SnapshotId:   state.Get("snapshot_image_id").(int),
		RegionNames:  state.Get("regions").([]string),
		Client:       client,
		StateData: map[string]interface{}{
			"generated_data":  state.Get("generated_data"),
			"source_image_id": state.Get("source_image_id"),
			"droplet_size":    state.Get("droplet_size"),
			"droplet_name":    state.Get("droplet_name"),
			"build_region":    state.Get("build_region"),
		},
	}

	return artifact, nil
}
