apiVersion: strange.company/v1alpha1
kind: Test
metadata:
  task: sc-9
  exemptionClass: documentation-only
spec:
  kind: required-facts
  checks:
    - id: J-1
      acIds: [AC-1]
      requiredFacts:
        paths: ["docs/guide.md"]
        mustMatch: ["30 days"]
    - id: J-2
      acIds: [AC-2]
      requiredFacts:
        paths: ["docs/guide.md"]
        mustMatch: ["30 days"]
