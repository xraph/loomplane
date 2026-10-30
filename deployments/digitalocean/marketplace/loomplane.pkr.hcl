# Builds the Loomplane DigitalOcean Marketplace 1-Click snapshot.
#
#   export DIGITALOCEAN_TOKEN=...
#   packer init .
#   packer build -var loomplane_version=v1.2.3 .

packer {
  required_plugins {
    digitalocean = {
      source  = "github.com/digitalocean/digitalocean"
      version = "~> 1.4"
    }
  }
}

variable "do_token" {
  type      = string
  default   = env("DIGITALOCEAN_TOKEN")
  sensitive = true
}

variable "loomplane_version" {
  type        = string
  description = "Release tag to bake into the image, such as v1.2.3."

  validation {
    condition     = can(regex("^v[0-9]+\\.[0-9]+\\.[0-9]+$", var.loomplane_version))
    error_message = "The loomplane_version variable must look like v1.2.3."
  }
}

variable "region" {
  type    = string
  default = "nyc3"
}

variable "size" {
  type    = string
  default = "s-2vcpu-4gb"
}

variable "image" {
  type    = string
  default = "ubuntu-24-04-x64"
}

source "digitalocean" "loomplane" {
  api_token     = var.do_token
  image         = var.image
  region        = var.region
  size          = var.size
  ssh_username  = "root"
  snapshot_name = "loomplane-${var.loomplane_version}"
  droplet_name  = "loomplane-image-build"
}

build {
  sources = ["source.digitalocean.loomplane"]

  # A fresh Droplet is still running cloud-init and unattended upgrades.
  provisioner "shell" {
    inline = ["cloud-init status --wait"]
  }

  provisioner "file" {
    source      = "../install.sh"
    destination = "/tmp/loomplane-install.sh"
  }

  provisioner "file" {
    source      = "../files/"
    destination = "/tmp/loomplane-files-base"
  }

  provisioner "file" {
    source      = "files/"
    destination = "/tmp/loomplane-files-image"
  }

  # Copy the trees into place and set modes explicitly: uploads don't keep them.
  provisioner "shell" {
    inline = [
      "cp -R /tmp/loomplane-files-base/. /",
      "cp -R /tmp/loomplane-files-image/. /",
      "chmod 0755 /usr/local/bin/loomplane-setup /etc/update-motd.d/99-loomplane",
      "chmod 0755 /var/lib/cloud/scripts/per-instance/001-loomplane-secrets",
      "chmod 0644 /etc/profile.d/loomplane-setup.sh",
      "chmod 0755 /tmp/loomplane-install.sh",
      "rm -rf /tmp/loomplane-files-base /tmp/loomplane-files-image",
    ]
  }

  # Install Docker and ufw, unpack the release, pull the images. No secrets
  # here: the first-boot script makes those per Droplet.
  provisioner "shell" {
    environment_vars = ["LOOMPLANE_VERSION=${var.loomplane_version}"]
    inline           = ["/tmp/loomplane-install.sh prepare", "rm -f /tmp/loomplane-install.sh"]
  }

  provisioner "shell" {
    environment_vars = ["DEBIAN_FRONTEND=noninteractive"]
    inline           = ["apt-get -y update", "apt-get -y upgrade"]
  }

  # DigitalOcean's own scripts: strip host keys, history and logs, then check
  # the image. A failed check fails the build.
  provisioner "shell" {
    scripts = [
      "scripts/90-cleanup.sh",
      "scripts/99-img-check.sh",
    ]
  }
}
