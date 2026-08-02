variable "YUANSHU_VERSION" {
  default = "dev"
}

group "default" {
  targets = ["server", "standalone"]
}

target "common" {
  context    = "."
  dockerfile = "Dockerfile"
  platforms  = ["linux/amd64"]
  labels = {
    "org.opencontainers.image.version" = YUANSHU_VERSION
  }
}

target "server" {
  inherits = ["common"]
  target   = "server"
  tags     = ["yuanshu-server:${YUANSHU_VERSION}"]
}

target "standalone" {
  inherits = ["common"]
  target   = "standalone"
  tags     = ["yuanshu-standalone:${YUANSHU_VERSION}"]
}
