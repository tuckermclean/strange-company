apiVersion: strange.company/v1alpha1
kind: Judge
metadata:
  task: sc-9
  exemptionClass: documentation-only
spec:
  kind: rubric
  checks:
    - id: J-1
      acIds: [AC-1, AC-2]
      rubric: The guide states the retention window.
