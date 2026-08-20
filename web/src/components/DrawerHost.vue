<script setup lang="ts">
import { useStore } from '@/store'
import InstanceDrawer from '@/views/drawers/InstanceDrawer.vue'
import AccountDrawer from '@/views/drawers/AccountDrawer.vue'
import AddAccountDrawer from '@/views/drawers/AddAccountDrawer.vue'
import CreateInstanceDrawer from '@/views/drawers/CreateInstanceDrawer.vue'

const { state } = useStore()
</script>

<template>
  <template v-if="state.drawer">
    <InstanceDrawer v-if="state.drawer.kind === 'instance'" :id="state.drawer.id" :tab="state.drawer.tab" />
    <AccountDrawer v-else-if="state.drawer.kind === 'account'" :id="state.drawer.id" :tab="state.drawer.tab" />
    <AddAccountDrawer v-else-if="state.drawer.kind === 'add-account'" />
    <CreateInstanceDrawer v-else-if="state.drawer.kind === 'create-instance'" />
  </template>
</template>
