package fake

// guardYAML is a byte-identical copy of infra/tofu/compute/fake/guard.yaml (the fake has no repository to read it
// from in unit tests); TestGuardCopyMatchesModule keeps the two in step.
const guardYAML = `# Plan guard of the fake compute module (plan 03 section 22, F09 03-AC-5 and 03-AC-6). The fake provider feeds a
# SYNTHETIC tofu show -json plan (src/internal/provider/fake PlanJSON) through this guard; the plan names a fake
# provider (registry.opentofu.org/strange-company/fake) and the resource type fake_node, not the built-in
# terraform_data of main.tf, because the guard only admits registry.opentofu.org sources. The fake costs nothing, so
# the resource is unpaid. src/internal/provider/fake keeps a byte-identical copy (guard_embed.go); a test checks it.
apiVersion: strange.company/v1alpha1
kind: TofuPlanGuard
metadata:
  name: fake-apply
spec:
  rootModule: infra/tofu/compute/fake
  allowedProviders:
    - registry.opentofu.org/strange-company/fake
  forbidReplace: true
  resources:
    - type: fake_node
      paid: false
      allowedActions: [no-op, read, create, update]
      maxCount: expectedNodeCount
`
