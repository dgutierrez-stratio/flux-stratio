@Library('libpipelines') _

hose {
    EMAIL = 'platform@stratio.com'
    BUILDTOOL = 'make'
    DEVTIMEOUT = 30
    BUILDTOOL_IMAGE = 'golang:1.26.0'
    VERSIONING_TYPE = 'semver'
    DEPLOYONPRS = true
    MODULE_LIST = [ "paas.flux-stratio:flux-stratio:tar.gz" ]

    DEV = { config ->
        doPackage(conf: config, parameters: "GOCACHE=/tmp")
        doDeploy(conf: config)
    }
}
