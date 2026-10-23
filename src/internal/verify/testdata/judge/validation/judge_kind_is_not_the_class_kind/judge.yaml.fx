apiVersion: strange.company/v1alpha1
kind: Judge
metadata:
  task: sc-9
  exemptionClass: documentation-only
spec:
  kind: benchmark
  checks:
    - id: J-1
      acIds: [AC-1]
      command:
        argv: ["go", "test", "-bench", "."]
        timeoutSeconds: 300
      threshold:
        metric: ns/op
        op: "<"
        value: 100
    - id: J-2
      acIds: [AC-2]
      command:
        argv: ["go", "test", "-bench", "."]
        timeoutSeconds: 300
      threshold:
        metric: ns/op
        op: "<"
        value: 100
