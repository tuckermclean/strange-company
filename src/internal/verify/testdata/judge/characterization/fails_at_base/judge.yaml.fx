apiVersion: strange.company/v1alpha1
kind: Judge
metadata:
  task: sc-9
  exemptionClass: refactor
spec:
  kind: characterization
  checks:
    - id: J-1
      acIds: [AC-1]
      command:
        argv: ["go", "test", "./pkg/..."]
        timeoutSeconds: 120
    - id: J-2
      acIds: [AC-2]
      command:
        argv: ["go", "test", "./pkg/..."]
        timeoutSeconds: 120
