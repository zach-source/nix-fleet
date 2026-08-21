package inventory

// Base OS identifiers accepted in host configs. These mirror the enum in
// modules/nixfleet/options.nix.
const (
	BaseUbuntu   = "ubuntu"
	BaseNixOS    = "nixos"
	BaseDarwin   = "darwin"
	BaseDGX      = "dgx"
	BaseSynology = "synology"
)

// NormalizeBase maps a base onto the base whose deployment mechanics it shares.
//
// DGX OS is an Ubuntu derivative — same apt, same systemd, same filesystem
// layout — so everything about *how* NixFleet deploys to it (flake attribute,
// nix profile path, activation script, closure copy) is identical to Ubuntu.
// Only its update policy differs, which SupportsOSUpdates answers separately.
//
// Call this at deployment sites that switch on base so a DGX host takes the
// Ubuntu path rather than falling through to "unknown base".
func NormalizeBase(base string) string {
	if base == BaseDGX {
		return BaseUbuntu
	}
	return base
}

// SupportsOSUpdates reports whether NixFleet may drive OS updates on this base.
//
// DGX returns false by our choice of ownership, not because apt is unsafe
// there. NVIDIA documents DGX Dashboard as the "primary and recommended" way to
// update a DGX Spark, and a full manual update is a two-part job — apt for the
// OS and packages, then fwupdmgr for firmware:
//
//	sudo apt update && sudo apt dist-upgrade
//	sudo fwupdmgr refresh && sudo fwupdmgr upgrade && sudo reboot
//
// NixFleet drives neither half. It has no firmware story at all, so an
// apt-only "update" here would be a partial one, and it would race the
// dashboard for the same packages. Leaving the whole lane to NVIDIA's tooling
// keeps one owner for updates instead of two.
//
// Deploying to a DGX is unaffected — see IsAptManaged.
//
// Ref: https://docs.nvidia.com/dgx/dgx-spark/os-and-component-update.html
func SupportsOSUpdates(base string) bool {
	return base == BaseUbuntu || base == BaseNixOS
}

// IsAptManaged reports whether packages on this base are managed with apt.
//
// True for DGX: installing and removing packages is exactly what we still want
// to do there, and is the reason DGX is worth modelling as a managed host at
// all rather than excluding it outright.
func IsAptManaged(base string) bool {
	return base == BaseUbuntu || base == BaseDGX
}

// SupportsOSUpdates reports whether OS updates may be driven on this host.
func (h *Host) SupportsOSUpdates() bool { return SupportsOSUpdates(h.Base) }

// IsAptManaged reports whether this host's packages are managed with apt.
func (h *Host) IsAptManaged() bool { return IsAptManaged(h.Base) }
