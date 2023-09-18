# Protocol Berg

This is a demo of the Kurtosis SDK that shows a multi client testnet with Proposer Builder Simulation (PBS) MEV components. The testnet 
launches multiple components including several EL CL clients as mentioned in `input_args.json`, MEV infra components, monitoring components
like grafana, prometheus, explorers and much more

Built using the [eth2-package](https://github.com/kurtosis-tech/eth2-package) and [Kurtosis](https://docs.kurtosis.com/)

# Run Instructions

1. Have [Kurtosis version 0.83.0 installed](https://docs.kurtosis.com/install-historical/)
2. Have Docker Installed
3. Have a running Kurtosis engine `kurtosis engine restart`
4. `go test -v -timeout=20m ./` or press play on your favorite IDE

# Further Kurtosis

1. You can see everything that is running using `kurtosis enclave inspect <enclve_name>`
2. You can see logs of services using `kurtosis servcie logs <enclave name> <service name>`
3. You can see shell into a service using `kurtosis servcie shell <enclave name> <service name>`

# MEV Notes

1. MEV Registration of validators should happen at slot 64
2. By Slot 128~ you should start seeing blocks being submitted